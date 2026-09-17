package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/objectstore/storefactory"
	"github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/retention"
	"github.com/F31/ppts/internal/storagelifecycle"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/traceprop"
	"github.com/F31/ppts/internal/usage"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
)

// runWorker 启动任务执行器（原 cmd/worker 的 run）。
func runWorker() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// 后台循环组件接受 *log.Logger，用 slog 后端适配器统一为结构化输出。
	stdLogger := slog.NewLogLogger(logger.Handler(), slog.LevelInfo)
	dsn := os.Getenv("PPTS_DATABASE_URL")
	tenantID := os.Getenv("PPTS_TENANT_ID")
	if dsn == "" {
		return errors.New("PPTS_DATABASE_URL is required")
	}
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
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	jobs, err := pipeline.NewPGStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer jobs.Close()

	// ADR-018 双连接：跨租户模式（未设 PPTS_TENANT_ID）下可用 PPTS_SCHEDULER_DATABASE_URL
	// 提供独立调度连接（ppts_scheduler 角色，仅权限受限的调度函数）；未提供则复用业务连接。
	var claimer pipeline.Claimer
	if tenantID == "" {
		if schedDSN := os.Getenv("PPTS_SCHEDULER_DATABASE_URL"); schedDSN != "" {
			claimer, err = pipeline.NewPGStore(ctx, schedDSN)
			if err != nil {
				return err
			}
			defer claimer.(*pipeline.PGStore).Close()
		}
	}

	policyStore := tenant.NewPGStore(pool)
	registry, err := storefactory.FromEnv(policyStore)
	if err != nil {
		return err
	}
	objects := objectstore.WithInventory(registry, policyStore)
	objects, err = storefactory.WithEnvelopeEncryptionFromEnv(objects, policyStore)
	if err != nil {
		return err
	}
	auditStore := audit.NewPGStore(pool)
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
	usageStore := usage.NewPGStore(pool).WithPriceBook(priceBook)
	scriptDraftHandler := app.NewScriptDraftHandler(narration.NewPGStore(pool), objects).WithPolisher(polisher).WithTokenAccounting(usageStore)
	if vision, ok := polisher.(llm.VisionExtractor); ok {
		scriptDraftHandler = scriptDraftHandler.WithVisualExtractor(vision)
	}
	metrics := observability.NewPipelineMetrics()
	ttsProvider := ttsProviderFromEnv()
	narrationHandler := app.NewNarrationHandler(narration.NewPGStore(pool), jobs, objects, ttsProvider).WithUsage(usageStore).WithTTSMetrics(metrics).WithDictionary(pronunciation.NewPGStore(pool))
	attachGateway(ctx, logger, pool, scriptDraftHandler, narrationHandler, polisher, ttsProvider)
	mp4Encoder, err := media.NewMP4Encoder()
	if err != nil {
		logger.Info("mp4 encoder unavailable", "error", err)
	}
	exportHandler := app.NewExportHandler(artifact.NewPGStore(pool), jobs, objects, mp4Encoder).
		WithSubtitleFont(subtitleFontName())
	dispatch := func(ctx context.Context, job *pipeline.Job) error {
		// 异步任务 span link：把 API 侧创建的 span 以 link 关联到 worker 执行 span（G3-8）。
		_, span := observability.Tracer("ppts.worker").Start(ctx, "job."+string(job.Kind),
			trace.WithLinks(trace.Link{SpanContext: traceprop.SpanContextFromTraceParent(job.TraceParent)}))
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

	// 保留与清理后台循环（G3-7）：孤儿上传清理 + 源文件到期/按需删除。
	sweeper := retention.NewSweeper(retention.NewPGStore(pool), objects, durationEnv("PPTS_UPLOAD_ABANDON_TTL", 24*time.Hour), stdLogger).
		WithQuotaReservationTTL(durationEnv("PPTS_QUOTA_RESERVATION_TTL", 24*time.Hour)).
		WithAuditor(auditStore)
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go runSweeper(sweepCtx, sweeper, durationEnv("PPTS_RETENTION_INTERVAL", time.Hour))

	// 队列积压 gauge（G3-8）：跨租户模式下优先用调度连接读取最老等待。
	var backlogAge observability.QueueAgeFunc
	if sched, ok := claimer.(*pipeline.PGStore); ok {
		backlogAge = sched.OldestQueuedAge
	} else {
		backlogAge = jobs.OldestQueuedAge
	}
	backlogReporter := observability.NewQueueBacklogReporter(metrics, backlogAge, durationEnv("PPTS_QUEUE_BACKLOG_INTERVAL", 30*time.Second), stdLogger)
	backlogCtx, stopBacklog := context.WithCancel(ctx)
	defer stopBacklog()
	go backlogReporter.Run(backlogCtx)

	// 审计保留/归档（G3-4）：到期审计事件先写对象存储，再清除。
	if retDays := envInt("PPTS_AUDIT_RETENTION_DAYS", 365); retDays > 0 {
		archiver := audit.NewArchiver(auditStore, retention.NewPGStore(pool), objects, stdLogger)
		archiveCtx, stopArchive := context.WithCancel(ctx)
		defer stopArchive()
		go runAuditArchiver(archiveCtx, archiver, time.Duration(retDays)*24*time.Hour,
			durationEnv("PPTS_AUDIT_ARCHIVE_INTERVAL", time.Hour))
	}

	storageSyncer := storagelifecycle.NewSyncer(policyStore, objects, stdLogger)
	storageLifecycleCtx, stopStorageLifecycle := context.WithCancel(ctx)
	defer stopStorageLifecycle()
	go runStorageLifecycleSyncer(storageLifecycleCtx, storageSyncer, durationEnv("PPTS_STORAGE_LIFECYCLE_INTERVAL", 6*time.Hour))

	if tenantID == "" {
		logger.Info("worker started", "mode", "cross-tenant", "owner", owner)
	} else {
		logger.Info("worker started", "mode", "tenant", "tenant_id", tenantID, "owner", owner)
	}
	runErr := worker.Run(ctx)
	if runErr != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return runErr
	}
	return nil
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

// subtitleFontName 返回字幕烧录锁定的字体族名；未设置时默认与 Dockerfile.worker 安装的
// fonts-wqy-zenhei 对齐，保证中文渲染在各部署环境一致可复现（V1_6 §338）。
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
func attachGateway(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, draft *app.ScriptDraftHandler, narr *app.NarrationHandler, envPolisher llm.TextRewriter, envTTS tts.TTSProvider) {
	cipher, err := gateway.CipherFromEnv()
	if err != nil {
		logger.Info("model gateway disabled (no AES key); using env providers")
		return
	}
	store := gateway.NewPGStore(pool, cipher)
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
func draftTenantPolisher(ctx context.Context, store *gateway.PGStore, providers *gateway.ProviderCache, tenantID string) (llm.TextRewriter, error) {
	cfg, err := store.Resolve(ctx, tenantID, gateway.KindLLM)
	if err != nil {
		return nil, err
	}
	return providers.LLM(cfg)
}

// - siliconflow：PPTS_TTS_BASE_URL / PPTS_TTS_API_KEY / PPTS_TTS_MODEL / PPTS_TTS_VOICE
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
