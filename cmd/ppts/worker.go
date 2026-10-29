package main

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations"
	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/internal/project"
	animrender "github.com/F31/ppts/internal/render"
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
		WithSourceRevisions(stores.projects).
		WithSegmentConcurrency(envInt("PPTS_NARRATION_TTS_CONCURRENCY", 0))
	// 文本规范化引擎：显式开关，默认关闭。启用时按租户/语言/配置有界缓存，TTL 到期重建，
	// 词典合并（租户优先+平台种子兜底）只在适配层发生，不改动 pronunciation.Store 语义。
	if textNormEnabled() {
		logger.Info("textnorm: enabling TTS text normalization engine")
		narrationHandler = narrationHandler.WithTextPreparation(app.AdaptTextNorm(cachedAppTextNorm(appTextNorm{store: stores.pronunciation, contextRules: stores.contextRule, logger: logger})))
	}
	attachGateway(ctx, logger, stores, scriptDraftHandler, narrationHandler, polisher, ttsProvider)
	mp4Encoder, err := media.NewMP4Encoder()
	if err != nil {
		logger.Info("mp4 encoder unavailable", "error", err)
	}
	maxExportMinutes := envInt("PPTS_MAX_EXPORT_DURATION_MINUTES", 60)
	if maxExportMinutes < 0 {
		maxExportMinutes = 0
	}
	exportHandler := app.NewExportHandler(stores.artifacts, jobs, objects, mp4Encoder).
		WithSubtitleFont(subtitleFontName()).
		WithNarrationWriter(integrations.NewGoPPTXNarrationWriter()).
		WithSourceRevisions(stores.projects).
		WithMaxDuration(time.Duration(maxExportMinutes) * time.Minute).
		WithLogger(logger)
	// 动画通道（ADR-21）：默认关闭，且要求「开关 + 服务地址」同时具备才接入。
	// 渲染服务是可选依赖——没有它，导出必须照常成功，所以关闭态不建客户端、不探测。
	// 与 api 同一份判定（见 animationEnabled）：以前这里只认 env，
	// 本地 profile 下 api 开了动画、worker 却不接，导出静默退回静态版。
	if animationEnabled(stores.isSQLite()) {
		if base := strings.TrimSpace(os.Getenv("PPTS_RENDERER_URL")); base != "" {
			client, cerr := animrender.NewClient(animrender.ClientConfig{
				BaseURL: base,
				Timeout: time.Duration(envInt("PPTS_RENDERER_TIMEOUT_SECONDS", 30)) * time.Second,
			})
			if cerr != nil {
				logger.Warn("animation: renderer client disabled", "error", cerr)
			} else {
				exportHandler = exportHandler.WithAnimation(client,
					time.Duration(envInt("PPTS_ANIMATION_TIMEOUT_MINUTES", 20))*time.Minute)
				exportHandler = wireAnimationPages(logger, exportHandler, base, stores.animation, objects)
			}
		} else {
			logger.Warn("animation: PPTS_ANIMATION_ENABLED is set but PPTS_RENDERER_URL is empty; staying static")
		}
	}
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

	// worker 进程单独暴露指标（影子分歧等计数在 worker 进程内自增；
	// api 的 /metrics 只反映 api 进程自身 expvar，看不到 worker 侧取值）。
	// 默认关闭；显式设置 PPTS_WORKER_METRICS_ADDR（如 127.0.0.1:8081）才监听，
	// 与 /metrics 的唯一准入一致：PPTS_METRICS_TOKEN 的 Bearer（fail-closed）。
	if addr := strings.TrimSpace(os.Getenv("PPTS_WORKER_METRICS_ADDR")); addr != "" {
		go serveWorkerMetrics(addr, stdLogger)
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
	// 队列观测（P2-B3）：ppts_worker_queue_oldest_wait_seconds 这个 gauge 与它的
	// QueueBacklogReporter 此前**从未被接线** —— 仪表盘上恒为 0，会被读成"从来没有积压"。
	// 0 表示"确实为零"而不是"没人采集"，这里的启动就是把这个假象修掉。
	go observability.NewQueueBacklogReporter(metrics, stores.jobs.OldestQueuedAge,
		durationEnv("PPTS_QUEUE_BACKLOG_INTERVAL", 30*time.Second), stdLogger).Run(runCtx)
	go observability.NewQueueDepthReporter(stores.jobs, tenantID,
		durationEnv("PPTS_QUEUE_DEPTH_INTERVAL", 30*time.Second), stdLogger).Run(runCtx)
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

// serveWorkerMetrics 在 worker 进程内暴露受 PPTS_METRICS_TOKEN 保护的指标端点。
// 进程隔离：worker 侧 expvar（队列、TTS、文本规范化词典等）只有本进程能读，
// 单独监听是让 worker 内部计数可被采集的唯一途径。
// 失败静默记录一行日志（不阻塞主流程），与守护进程重启策略配合自动恢复。
func serveWorkerMetrics(addr string, stdLogger *log.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !observability.MetricsTokenOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		observability.PrometheusHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /debug/vars", func(w http.ResponseWriter, r *http.Request) {
		if !observability.MetricsTokenOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		expvar.Handler().ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: addr, Handler: mux}
	stdLogger.Printf("worker metrics listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stdLogger.Printf("worker metrics server: %v", err)
	}
}

// startBackgroundLoops 启动 PostgreSQL 控制面后台循环（保留清理/审计归档/存储生命周期）。
func startBackgroundLoops(ctx context.Context, stores *storeSet, objects objectstore.ObjectStore, auditStore audit.Store, stdLogger *log.Logger) {
	sweeper := retention.NewSweeper(retention.NewPGStore(stores.pg), objects, durationEnv("PPTS_UPLOAD_ABANDON_TTL", 24*time.Hour), stdLogger).
		WithQuotaReservationTTL(durationEnv("PPTS_QUOTA_RESERVATION_TTL", 24*time.Hour)).
		// 孤儿扫描默认开启但**只报告**：删对象不可逆，而"无引用"的依据是"归属表里查不到引用行"，
		// 漏认一种引用关系就会删掉在用对象。人工核对审计里的 orphan.detected 后再开 PPTS_ORPHAN_DELETE。
		WithOrphanScan(durationEnv("PPTS_ORPHAN_GRACE", 24*time.Hour), boolEnv("PPTS_ORPHAN_DELETE", false)).
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

// boolEnv 读取布尔环境变量；仅 "1"/"true"/"yes" 为真（忽略大小写与首尾空白）。
// 未设置或无法识别时一律取 fallback —— 危险开关的默认必须是"关"。
func boolEnv(name string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if raw == "" {
		return fallback
	}
	return raw == "1" || raw == "true" || raw == "yes"
}

// subtitleFontName 返回字幕烧录锁定的字体族名。
func subtitleFontName() string {
	if v := os.Getenv("PPTS_SUBTITLE_FONT_NAME"); v != "" {
		return v
	}
	return "WenQuanYi Zen Hei"
}

// envInt 读取整数环境变量，非法或缺失时返回默认值。
// wireAnimationPages 接入「页图投递 + 规格取回」（tasks 2.0）。
//
// 单机部署用本地共享目录：导出侧把页图写进 PPTS_ANIMATION_ASSET_DIR，渲染容器把**同一个卷**
// 挂成自己的 -assets-dir。多机 / S3 部署只需要换一个 AssetPublisher 实现（把字节送到渲染服务
// 能读的地方，返回同一个相对键），这条接线与导出主路径都不用改。
//
// 任何一步缺失都退化为静态导出而不是硬失败：渲染服务是可选依赖，没有它导出必须照常成功。
func wireAnimationPages(logger *slog.Logger, h *app.ExportHandler, rendererURL string, specs animstore.Store, objects objectstore.ObjectStore) *app.ExportHandler {
	dir := strings.TrimSpace(os.Getenv("PPTS_ANIMATION_ASSET_DIR"))
	if dir == "" {
		logger.Warn("animation: PPTS_ANIMATION_ASSET_DIR is empty; page images cannot reach the renderer, exports stay static",
			"renderer", rendererURL)
		return h
	}
	publisher, err := app.NewLocalAssetPublisher(dir)
	if err != nil {
		logger.Warn("animation: asset publisher disabled; exports stay static", "error", err)
		return h
	}
	if specs == nil {
		logger.Warn("animation: animation spec store is unavailable; exports stay static")
		return h
	}
	assembler := app.NewAnimationPageAssembler(publisher, specs).WithLogger(logger)
	// 元素几何来自解析期的 extracted document（tasks 2.0b）：按源版本读一次，
	// 不新增产物、不重解析 PPTX。没有对象存储时退化为「只编整页轨道」——
	// 那是能力边界而不是错误，导出照常成功。
	withGeometry := false
	if objects != nil {
		assembler = assembler.WithGeometry(app.NewDocumentGeometry(objects))
		withGeometry = true
	}
	h = h.WithAnimationPages(assembler.Build)
	if !withGeometry {
		logger.Warn("animation: element geometry unavailable (no object store); pages get page-level tracks only")
	}
	logger.Info("animation: page assembly wired via shared asset dir",
		"renderer", rendererURL, "asset_dir", publisher.Root(), "element_geometry", withGeometry)
	return h
}

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
