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
	"strings"
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
	// TTS 供应商必须在启动期确定：未设置/拼错/未知值一律启动失败，
	// 不允许回落假供应商（FakeProvider 产出静音 WAV，配音 job 仍判 succeeded、
	// 用户拿到无声成品，且 ppts_tts_synthesis_total 计入成功——连指标都是假的）。
	// fake 是**显式开关**而非兜底：只有明确指定 PPTS_TTS_PROVIDER=fake 才启用，
	// 并同时输出告警与 ppts_tts_fake_provider_active 指标。
	ttsProvider, err := ttsProviderFromEnv(logger)
	if err != nil {
		return err
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
	// 渲染器（LibreOffice + poppler）缺失时，解析仍会"成功"，但没有页面图
	// → 编辑器无缩略图、播放无帧图、MP4 导出无素材，而用户侧原本零信号。
	// 治理三件套：① 指标 ppts_render_pages_disabled ② Warn 日志 ③ PPTS_REQUIRE_RENDERER
	// 可让关键部署启动即失败；产物侧另有 pages.json 写入 renderer="unavailable" 留痕。
	renderer, err := render.NewSofficeRenderer()
	if err != nil {
		observability.SetRenderPagesDisabled(true)
		if envBool("PPTS_REQUIRE_RENDERER", false) {
			return fmt.Errorf("renderer required (PPTS_REQUIRE_RENDERER=true) but unavailable: %w", err)
		}
		logger.Warn("renderer unavailable: page images disabled - editor thumbnails, playback frames and MP4 export sources will be missing",
			"error", err,
			"hint", "install libreoffice + poppler-utils; set PPTS_REQUIRE_RENDERER=true to fail fast instead")
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
	scriptDraftHandler := app.NewScriptDraftHandler(stores.scripts, objects).WithPolisher(polisher).WithTokenAccounting(usageStore).WithSteps(jobs).
		WithSlideNotes(project.NewPgSlideNotesStore(objects)).
		WithConcurrency(envInt("PPTS_SCRIPT_DRAFT_CONCURRENCY", 3))
	if !envBool("PPTS_SCRIPT_DRAFT_CACHE", true) {
		scriptDraftHandler = scriptDraftHandler.WithCacheDisabled()
	}
	// 视觉锚点是 LLM 配置的能力开关（见 attachGateway 的 WithTenantVision）。
	// 此 env 路径仅用于未启用网关（无 AES key）的纯环境变量部署：显式设置 PPTS_LLM_VISION_MODEL 才接入。
	if strings.TrimSpace(os.Getenv("PPTS_LLM_VISION_MODEL")) != "" {
		if vision, ok := polisher.(llm.VisionExtractor); ok {
			scriptDraftHandler = scriptDraftHandler.WithVisualExtractor(vision)
		}
	}
	metrics := observability.NewPipelineMetrics()
	narrationHandler := app.NewNarrationHandler(stores.scripts, jobs, objects, ttsProvider).
		WithUsage(usageStore).WithTTSMetrics(metrics).WithDictionary(stores.pronunciation).
		WithSourceRevisions(stores.projects)
	// 文本规范化引擎（V2.8）：显式开关，默认关闭。启用时按 (租户, 语言) 逐任务构建，
	// 词典合并（租户优先+平台种子兜底）只在适配层发生，不改动 pronunciation.Store 语义。
	if textNormEnabled() {
		logger.Info("textnorm: enabling TTS text normalization engine")
		narrationHandler = narrationHandler.WithTextNorm(appTextNorm{store: stores.pronunciation, logger: logger})
	}
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
		Metrics:     metrics,
		Logger:      stdLogger,
		Claimer:     claimer,
		MaxAttempts: envInt("PPTS_WORKER_MAX_ATTEMPTS", 10),
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

	// 任务级并发：单进程内跑 N 个执行循环（领取已加进程内互斥，处理仍并发）。
	// 默认 2：让「一键成稿」与「配音」等任务可并行，而不是排队；SQLite 单写者可调小到 1。
	workerConcurrency := envInt("PPTS_WORKER_CONCURRENCY", 2)
	if workerConcurrency < 1 {
		workerConcurrency = 1
	}
	if tenantID == "" {
		logger.Info("worker started", "mode", "cross-tenant", "owner", owner, "driver", stores.cfg.Driver, "concurrency", workerConcurrency)
	} else {
		logger.Info("worker started", "mode", "tenant", "tenant_id", tenantID, "owner", owner, "driver", stores.cfg.Driver, "concurrency", workerConcurrency)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	errCh := make(chan error, workerConcurrency)
	for i := 0; i < workerConcurrency; i++ {
		go func() { errCh <- worker.Run(runCtx) }()
	}
	var runErr error
	for i := 0; i < workerConcurrency; i++ {
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) && runErr == nil {
			runErr = err
			cancelRun() // 任一执行器致命失败即整体停机，交由上层重启/告警
		}
	}
	if runErr != nil {
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

// envBool 读取布尔环境变量（1/true/yes/on，大小写不敏感）；缺失或非法返回 fallback。
func envBool(name string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
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
		// 视觉锚点是 LLM 配置的能力开关：该租户的 LLM 配置 enabled 且 vision_model 非空时开启，
		// 留空即关闭（返回 nil,nil，不报错）。无需单独的类型或环境变量。
		cfg, err := store.Resolve(ctx, tenantID, gateway.KindLLM)
		if err != nil {
			if errors.Is(err, gateway.ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if strings.TrimSpace(cfg.VisionModel) == "" {
			return nil, nil
		}
		p, err := providers.LLM(cfg)
		if err != nil || p == nil {
			return nil, err
		}
		// SiliconFlowProvider 始终实现 VisionExtractor（多模态能力由所用模型决定）。
		return p, nil
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

// ttsProviderFromEnv 按 PPTS_TTS_PROVIDER 构建 TTS 供应商。
// siliconflow 需要 PPTS_TTS_BASE_URL / PPTS_TTS_API_KEY / PPTS_TTS_MODEL / PPTS_TTS_VOICE。
//
// 未设置 / 拼错 / 未知值一律返回错误（启动失败），绝不静默回落 fake——
// 静音 WAV 与"合成成功"在 UI 上不可区分，属于典型的假成功。fake 只能显式请求。
func ttsProviderFromEnv(logger *slog.Logger) (tts.TTSProvider, error) {
	switch provider := os.Getenv("PPTS_TTS_PROVIDER"); provider {
	case "siliconflow":
		return tts.NewSiliconFlowProvider(tts.SiliconFlowConfig{
			BaseURL: os.Getenv("PPTS_TTS_BASE_URL"),
			APIKey:  os.Getenv("PPTS_TTS_API_KEY"),
			Model:   os.Getenv("PPTS_TTS_MODEL"),
			Voice:   os.Getenv("PPTS_TTS_VOICE"),
		}), nil
	case "fake":
		logger.Warn("TTS fake provider active: generated narration is SILENT audio; this is not suitable for any non-development use",
			"hint", "set PPTS_TTS_PROVIDER=siliconflow with PPTS_TTS_API_KEY for real synthesis")
		observability.SetTTSFakeProviderActive(true)
		return tts.NewFakeProvider(), nil
	default:
		return nil, fmt.Errorf("unsupported PPTS_TTS_PROVIDER=%q (supported: fake, siliconflow); refusing to run because failure to configure TTS must not silently produce silent audio", provider)
	}
}
