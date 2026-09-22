package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/retention"
	"github.com/F31/ppts/internal/storagelifecycle"
	"github.com/F31/ppts/internal/usage"
)

// runWorker 启动任务执行器（原 cmd/worker 的 run）。
// 数据库由 PPTS_DB_DRIVER 选择：sqlite（单租户，本地租户上下文）或 postgres（多租户）。
// SQLite 单租户下跳过依赖全局控制面的后台循环（保留清理/审计归档/存储生命周期）。
func runWorker() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	stdLogger := slog.NewLogLogger(logger.Handler(), slog.LevelInfo)
	switch provider := os.Getenv("PPTS_TTS_PROVIDER"); provider {
	case "fake", "siliconflow":
		// 支持的供应商：fake=开发/测试；siliconflow=正式（需 PPTS_TTS_API_KEY）。
	default:
		return fmt.Errorf("unsupported PPTS_TTS_PROVIDER=%q (supported: fake, siliconflow)", provider)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	traceShutdown, err := observability.InitTracing()
	if err != nil {
		return err
	}
	defer func() { _ = traceShutdown(ctx) }()

	stores, err := openStores(ctx)
	if err != nil {
		return err
	}
	defer stores.close()

	jobs := stores.jobs
	tenantID := stores.defaultWorkerTenant(os.Getenv("PPTS_TENANT_ID"))

	// ADR-018 双连接（仅 PostgreSQL 跨租户模式）：可用 PPTS_SCHEDULER_DATABASE_URL
	// 提供独立调度连接（ppts_scheduler 角色，仅权限受限的调度函数）；未提供则复用业务连接。
	var claimer pipeline.Claimer
	if !stores.isSQLite() && tenantID == "" {
		if schedDSN := os.Getenv("PPTS_SCHEDULER_DATABASE_URL"); schedDSN != "" {
			schedStore, err := pipeline.NewPGStore(ctx, schedDSN)
			if err != nil {
				return err
			}
			defer schedStore.Close()
			claimer = schedStore
		}
	}

	objects := stores.objects
	auditStore := stores.audit
	parseHandler := app.NewParseHandler(objects, project.NewGoPPTXReader(project.Limits{})).WithSteps(jobs)
	if renderer, err := render.NewSofficeRenderer(); err != nil {
		logger.Info("renderer unavailable; page images disabled", "error", err)
	} else {
		parseHandler = parseHandler.WithRenderer(renderer)
		logger.Info("renderer enabled", "version", renderer.Version())
	}
	polisher, err := llm.FromEnv()
	if err != nil {
		return err
	}
	priceBook, err := pricing.FromEnv()
	if err != nil {
		return err
	}
	stores.applyPriceBook(priceBook)
	usageStore := stores.usage
	scriptDraftHandler := app.NewScriptDraftHandler(stores.scripts, objects).WithPolisher(polisher).WithTokenAccounting(usageStore).WithSteps(jobs)
	if vision, ok := polisher.(llm.VisionExtractor); ok {
		scriptDraftHandler = scriptDraftHandler.WithVisualExtractor(vision)
	}
	metrics := observability.NewPipelineMetrics()
	ttsProvider := ttsProviderFromEnv()
	narrationHandler := app.NewNarrationHandler(stores.scripts, jobs, objects, ttsProvider).
		WithUsage(usageStore).WithTTSMetrics(metrics).WithDictionary(stores.pronunciation)
	attachGateway(ctx, logger, stores, scriptDraftHandler, narrationHandler, polisher, ttsProvider)
	mp4Encoder, err := media.NewMP4Encoder()
	if err != nil {
		logger.Info("mp4 encoder unavailable", "error", err)
	}
	exportHandler := app.NewExportHandler(stores.artifacts, jobs, objects, mp4Encoder).
		WithSubtitleFont(subtitleFontName())
	dispatch := func(ctx context.Context, job *pipeline.Job) error {
		_, span := observability.Tracer("ppts.worker").Start(ctx, "job."+string(job.Kind))
		defer span.End()
		switch job.Kind {
		case pipeline.KindParse:
			return parseHandler.Handle(ctx, job)
		case pipeline.KindScriptDraft:
			return scriptDraftHandler.Handle(ctx, job)
		case pipeline.KindNarration:
			return narrationHandler.Handle(ctx, job)
		case pipeline.KindExport:
			return exportHandler.Handle(ctx, job)
		default:
			return fmt.Errorf("worker: unsupported job kind %q", job.Kind)
		}
	}
	hostname, _ := os.Hostname()
	owner := hostname + "-" + strconv.Itoa(os.Getpid())
	worker := pipeline.NewWorker(jobs, owner, tenantID, dispatch, pipeline.WorkerOptions{
		Metrics: metrics,
		Logger:  stdLogger,
		Claimer: claimer,
		OnCanceled: func(ctx context.Context, job *pipeline.Job) error {
			if job.Kind != pipeline.KindNarration || job.IDempotencyKey == "" {
				return nil
			}
			err := usageStore.Release(ctx, job.TenantID, job.IDempotencyKey, usage.KindGenSeconds)
			if errors.Is(err, usage.ErrReservationNotFound) || errors.Is(err, usage.ErrReservationReleased) || errors.Is(err, usage.ErrReservationSettled) {
				return nil
			}
			return err
		},
	})

	// 后台循环（保留清理/审计归档/存储生命周期）依赖控制面全租户视图，仅 PostgreSQL 启用。
	if !stores.isSQLite() {
		startBackgroundLoops(ctx, stores, objects, auditStore, stdLogger)
	}

	if tenantID == "" {
		logger.Info("worker started", "mode", "cross-tenant", "owner", owner, "driver", stores.cfg.Driver)
	} else {
		logger.Info("worker started", "mode", "tenant", "tenant_id", tenantID, "owner", owner, "driver", stores.cfg.Driver)
	}
	runErr := worker.Run(ctx)
	if runErr != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return runErr
	}
	return nil
}

// startBackgroundLoops 启动 PostgreSQL 控制面后台循环（保留清理/审计归档/存储生命周期）。
func startBackgroundLoops(ctx context.Context, stores *storeSet, objects objectstore.ObjectStore, auditStore audit.Store, stdLogger *log.Logger) {
	sweeper := retention.NewSweeper(retention.NewPGStore(stores.pg), objects, durationEnv("PPTS_UPLOAD_ABANDON_TTL", 24*time.Hour), stdLogger).
		WithQuotaReservationTTL(durationEnv("PPTS_QUOTA_RESERVATION_TTL", 24*time.Hour)).
		WithAuditor(auditStore)
	go runSweeper(ctx, sweeper, durationEnv("PPTS_RETENTION_INTERVAL", time.Hour))

	if retDays := envInt("PPTS_AUDIT_RETENTION_DAYS", 365); retDays > 0 {
		archiver := audit.NewArchiver(auditStore, retention.NewPGStore(stores.pg), objects, stdLogger)
		go runAuditArchiver(ctx, archiver, time.Duration(retDays)*24*time.Hour,
			durationEnv("PPTS_AUDIT_ARCHIVE_INTERVAL", time.Hour))
	}

	storageSyncer := storagelifecycle.NewSyncer(stores.tenant, objects, stdLogger)
	go runStorageLifecycleSyncer(ctx, storageSyncer, durationEnv("PPTS_STORAGE_LIFECYCLE_INTERVAL", 6*time.Hour))
}

// runSweeper 启动即执行一次，随后按 interval 周期清理。
func runSweeper(ctx context.Context, s *retention.Sweeper, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_ = s.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Sweep(ctx)
		}
	}
}

func runAuditArchiver(ctx context.Context, a *audit.Archiver, retention time.Duration, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_ = a.ArchiveBefore(ctx, time.Now().Add(-retention))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = a.ArchiveBefore(ctx, time.Now().Add(-retention))
		}
	}
}

func runStorageLifecycleSyncer(ctx context.Context, s *storagelifecycle.Syncer, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_ = s.Sync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Sync(ctx)
		}
	}
}

// durationEnv 读取时长环境变量，非法或缺失时返回默认值。
func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// subtitleFontName 返回字幕烧录锁定的字体族名。
func subtitleFontName() string {
	if v := os.Getenv("PPTS_SUBTITLE_FONT_NAME"); v != "" {
		return v
	}
	return "WenQuanYi Zen Hei"
}

// envInt 读取整数环境变量，非法或缺失时返回默认值。
func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

// attachGateway 接入模型网关（G3 可视化配置）：DB 有配置时优先，否则回退 env 供应商。
// 加密密钥未配置或 DB 不可用时静默回退 env，不阻断 worker 启动。
func attachGateway(ctx context.Context, logger *slog.Logger, stores *storeSet, draft *app.ScriptDraftHandler, narr *app.NarrationHandler, envPolisher llm.TextRewriter, envTTS tts.TTSProvider) {
	cipher, err := gateway.CipherFromEnv()
	if err != nil {
		logger.Info("model gateway disabled (no AES key); using env providers")
		return
	}
	store := stores.gatewayStore(cipher)
	providers := gateway.NewProviderCache()
	if err := gateway.SeedFromEnv(ctx, store, gateway.EnvFromEnv()); err != nil {
		logger.Warn("gateway seed from env failed", "error", err)
	}
	draft.WithTenantPolisher(func(ctx context.Context, tenantID string) (llm.TextRewriter, error) {
		if cfg, err := store.Resolve(ctx, tenantID, gateway.KindLLM); err == nil {
			return providers.LLM(cfg)
		}
		if envPolisher == nil {
			return nil, errors.New("worker: no LLM provider configured (env or gateway)")
		}
		return envPolisher, nil
	})
	draft.WithTenantVision(func(ctx context.Context, tenantID string) (llm.VisionExtractor, error) {
		p, err := draftTenantPolisher(ctx, store, providers, tenantID)
		if err != nil || p == nil {
			return nil, err
		}
		if v, ok := p.(llm.VisionExtractor); ok {
			return v, nil
		}
		return nil, errors.New("worker: LLM provider does not support vision")
	})
	narr.WithTenantProvider(func(ctx context.Context, tenantID string) (tts.TTSProvider, error) {
		if cfg, err := store.Resolve(ctx, tenantID, gateway.KindTTS); err == nil {
			return providers.TTS(cfg), nil
		}
		if envTTS == nil {
			return nil, errors.New("worker: no TTS provider configured (env or gateway)")
		}
		return envTTS, nil
	})
	logger.Info("model gateway attached", "ttl", "30s")
}

// draftTenantPolisher 与 attachGateway 的 LLM 解析保持一致（供 vision 复用同一实例）。
func draftTenantPolisher(ctx context.Context, store gateway.StoreResolver, providers *gateway.ProviderCache, tenantID string) (llm.TextRewriter, error) {
	cfg, err := store.Resolve(ctx, tenantID, gateway.KindLLM)
	if err != nil {
		return nil, err
	}
	return providers.LLM(cfg)
}

// ttsProviderFromEnv 按 PPTS_TTS_PROVIDER 构建 TTS 供应商。
// siliconflow 需要 PPTS_TTS_BASE_URL / PPTS_TTS_API_KEY / PPTS_TTS_MODEL / PPTS_TTS_VOICE。
func ttsProviderFromEnv() tts.TTSProvider {
	switch os.Getenv("PPTS_TTS_PROVIDER") {
	case "siliconflow":
		return tts.NewSiliconFlowProvider(tts.SiliconFlowConfig{
			BaseURL: os.Getenv("PPTS_TTS_BASE_URL"),
			APIKey:  os.Getenv("PPTS_TTS_API_KEY"),
			Model:   os.Getenv("PPTS_TTS_MODEL"),
			Voice:   os.Getenv("PPTS_TTS_VOICE"),
		})
	default:
		return tts.NewFakeProvider()
	}
}
