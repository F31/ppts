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
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/config"
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
	"github.com/F31/ppts/internal/share"
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
	// 没有 env 供应商不等于没有 TTS：租户在 Web 里配的模型网关（DB 行）本身就能提供，
	// 那条路不需要 PPTS_TTS_PROVIDER。但只要两条路都没有，就必须启动失败——
	// 静音 WAV 与"合成成功"在界面上不可区分，那是典型的假成功。
	if err := requireTTSProvider(ttsProvider); err != nil {
		return err
	}
	if ttsProvider == nil {
		logger.Info("TTS provider comes from model gateway only (no env provider set)")
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
	tenantID := stores.defaultWorkerTenant(config.String("PPTS_TENANT_ID", ""))

	// ADR-018 双连接（仅 PostgreSQL 跨租户模式）：可用 PPTS_SCHEDULER_DATABASE_URL
	// 提供独立调度连接（ppts_scheduler 角色，仅权限受限的调度函数）；未提供则复用业务连接。
	var claimer pipeline.Claimer
	if !stores.isSQLite() && tenantID == "" {
		if schedDSN := config.String("PPTS_SCHEDULER_DATABASE_URL", ""); schedDSN != "" {
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
	// WithProjects 是**必需**的：解析 handler 用它在成功后把真实页数回写 source_revisions.page_count。
	// 此前没接，解析一直"成功"、page_count 一直是 0——PPT 列表的"页数"列恒为 0，
	// 而没有任何一处报错。导入报告按版本对账时把这个断点暴露了出来（回写 0 != 解析 14 页）。
	parseHandler := app.NewParseHandler(objects, project.NewGoPPTXReader(project.Limits{})).
		WithSteps(jobs).
		WithProjects(stores.projects)
	// 渲染器（LibreOffice + poppler）缺失时，解析仍会"成功"，但没有页面图
	// → 编辑器无缩略图、播放无帧图、MP4 导出无素材，而用户侧原本零信号。
	// 治理三件套：① 指标 ppts_render_pages_disabled ② Warn 日志 ③ PPTS_REQUIRE_RENDERER
	// 可让关键部署启动即失败；产物侧另有 pages.json 写入 renderer="unavailable" 留痕。
	renderer, err := render.NewSofficeRenderer()
	if err != nil {
		observability.SetRenderPagesDisabled(true)
		if config.Bool("PPTS_REQUIRE_RENDERER", false) {
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
		WithConcurrency(config.Int("PPTS_SCRIPT_DRAFT_CONCURRENCY", 3))
	if !config.Bool("PPTS_SCRIPT_DRAFT_CACHE", true) {
		scriptDraftHandler = scriptDraftHandler.WithCacheDisabled()
	}
	// 视觉锚点是 LLM 配置的能力开关（见 attachGateway 的 WithTenantVision）。
	// 此 env 路径仅用于未启用网关（无 AES key）的纯环境变量部署：显式设置 PPTS_LLM_VISION_MODEL 才接入。
	if config.String("PPTS_LLM_VISION_MODEL", "") != "" {
		if vision, ok := polisher.(llm.VisionExtractor); ok {
			scriptDraftHandler = scriptDraftHandler.WithVisualExtractor(vision)
		}
	}
	metrics := observability.NewPipelineMetrics()
	narrationHandler := app.NewNarrationHandler(stores.scripts, jobs, objects, ttsProvider).
		WithUsage(usageStore).WithTTSMetrics(metrics).WithDictionary(stores.pronunciation).
		WithSourceRevisions(stores.projects).
		WithSegmentConcurrency(config.Int("PPTS_NARRATION_TTS_CONCURRENCY", 0))
	// 文本规范化引擎：显式开关，默认关闭。启用时按租户/语言/配置有界缓存，TTL 到期重建，
	// 词典合并（租户优先+平台种子兜底）只在适配层发生，不改动 pronunciation.Store 语义。
	if textNormEnabled() {
		logger.Info("textnorm: enabling TTS text normalization engine")
		narrationHandler = narrationHandler.WithTextPreparation(app.AdaptTextNorm(cachedAppTextNorm(appTextNorm{store: stores.pronunciation, contextRules: stores.contextRule, logger: logger})))
	}
	// 动画预填（M1b）：只有 script 档会真的调模型，结果按内容指纹写回对象存储。
	// 与 script_draft 共用同一套 token 记账——预填不许绕开额度。
	prefillHandler := app.NewAnimationPrefillHandler(objects).WithTokenAccounting(usageStore)
	attachGateway(ctx, logger, stores, scriptDraftHandler, narrationHandler, polisher, ttsProvider, prefillHandler)
	mp4Encoder, err := media.NewMP4Encoder()
	if err != nil {
		logger.Info("mp4 encoder unavailable", "error", err)
	}
	// 页图 WebP（ADR-034）：与 mp4Encoder 同一个 ffmpeg，但"有 ffmpeg"≠"带 libwebp"
	// （精简构建可能没有），所以单独探测；失败只是成品包页图退回 JPEG q82。
	//
	// 必须显式转成接口再判空：把 (*WebPPageEncoder)(nil) 直接塞进接口会得到
	// 「非 nil 的接口」，打包器据此对空壳调用，表现是每页一条假失败日志。
	var pageImageEncoder share.PageImageEncoder
	if enc, werr := media.NewWebPPageEncoder(); werr != nil {
		logger.Info("webp page encoder unavailable; bundle pages stay jpeg", "error", werr)
	} else {
		pageImageEncoder = enc
	}
	maxExportMinutes := config.Int("PPTS_MAX_EXPORT_DURATION_MINUTES", 60)
	if maxExportMinutes < 0 {
		maxExportMinutes = 0
	}
	exportHandler := app.NewExportHandler(stores.artifacts, jobs, objects, mp4Encoder).
		WithSubtitleFont(subtitleFontName()).
		WithPageImageEncoder(pageImageEncoder).
		WithNarrationWriter(integrations.NewGoPPTXNarrationWriter()).
		WithSourceRevisions(stores.projects).
		WithMaxDuration(time.Duration(maxExportMinutes) * time.Minute).
		// 动画渲染分钟按实际渲了的页结算（M4 §7.5）：预占在 api 受理时，
		// 只有这里知道哪几页降级成了静态段。
		WithUsage(usageStore).
		WithLogger(logger)
	// 本地 LibreOffice 渲染器（graceful）：用于带 Crop 镜头页的 300 DPI 重渲，使特写清晰。
	// 不可用（未安装）时保持 nil，导出退回 150 DPI 模糊特写路径，不影响整包。
	if sr, serr := render.NewSofficeRenderer(); serr != nil {
		logger.Info("slide renderer unavailable; close-up shots fall back to 150dpi blur path", "error", serr)
	} else {
		exportHandler = exportHandler.WithSlideRenderer(sr)
	}
	// 渲染服务（可选依赖，没有它导出必须照常成功）。地址配了就建客户端：
	//
	//   - **字体子集**（M6-4）需要它，且与动画无关——一份没有动画的静态成品包，
	//     字幕同样要内嵌字体才在别人机器上不出方框。所以这一路无条件接通。
	//   - **动画通道**（ADR-21）额外受开关约束：开关 + 地址同时具备才接入。
	//
	// 建客户端不发请求（Health 才探测），所以「动画关闭」多建一个客户端没有副作用。
	// 反过来，若把两件事都塞进 animationEnabled，默认关闭的动画开关就会连带
	// 关掉字体子集，而「开关关闭」只该关掉动画这一件事。
	if base := config.String("PPTS_RENDERER_URL", ""); base != "" {
		client, cerr := animrender.NewClient(animrender.ClientConfig{
			BaseURL: base,
			Timeout: time.Duration(config.Int("PPTS_RENDERER_TIMEOUT_SECONDS", 30)) * time.Second,
		})
		if cerr != nil {
			logger.Warn("renderer client disabled", "error", cerr)
		} else {
			exportHandler = exportHandler.WithRenderer(client)
			if animationEnabled(stores.isSQLite()) {
				exportHandler = exportHandler.WithAnimation(client,
					time.Duration(config.Int("PPTS_ANIMATION_TIMEOUT_MINUTES", 20))*time.Minute)
				exportHandler = wireAnimationPages(logger, exportHandler, base, stores.animation, objects)
			}
		}
	} else if animationEnabled(stores.isSQLite()) {
		logger.Warn("animation: PPTS_ANIMATION_ENABLED is set but PPTS_RENDERER_URL is empty; staying static")
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
		case pipeline.KindAnimationPrefill:
			return prefillHandler.Handle(ctx, job)
		default:
			return fmt.Errorf("worker: unsupported job kind %q", job.Kind)
		}
	}
	hostname, _ := os.Hostname()
	owner := hostname + "-" + strconv.Itoa(os.Getpid())
	// 取消与失败终态的善后：把已经预占的额度放掉。
	//
	// 两者共用一个实现：漏掉任何一个都会把额度锁到账期结束，而用户看到的是
	// "这个月没额度了"，实际一次都没渲成功——这类故障最难自查。
	releaseUsage := func(ctx context.Context, job *pipeline.Job) error {
		if job == nil || job.IDempotencyKey == "" {
			return nil
		}
		var err error
		switch job.Kind {
		case pipeline.KindNarration:
			err = usageStore.Release(ctx, job.TenantID, job.IDempotencyKey, usage.KindGenSeconds)
		case pipeline.KindExport:
			// 只有带动画的导出才预占过；静态导出 Release 会返回 NotFound，属正常。
			err = usageStore.Release(ctx, job.TenantID, job.IDempotencyKey, animationMinutesKind(job))
		case pipeline.KindAnimationPrefill:
			// 预填的预占发生在任务内部（见 app.AnimationPrefillHandler），逻辑操作 id 由
			// app.PrefillOpID 单点定义：两处各拼一次字符串，改了一处就会把额度锁到账期结束。
			err = usageStore.Release(ctx, job.TenantID, app.PrefillOpID(job.ID), usage.KindLLMTokens)
		default:
			return nil
		}
		if errors.Is(err, usage.ErrReservationNotFound) || errors.Is(err, usage.ErrReservationReleased) || errors.Is(err, usage.ErrReservationSettled) {
			return nil
		}
		return err
	}
	worker := pipeline.NewWorker(jobs, owner, tenantID, dispatch, pipeline.WorkerOptions{
		Metrics:     metrics,
		Logger:      stdLogger,
		Claimer:     claimer,
		MaxAttempts: config.Int("PPTS_WORKER_MAX_ATTEMPTS", 10),
		OnCanceled:  releaseUsage,
		// 失败终态也要释放：渲染失败（服务不可达、超时）不会产生渲染分钟，
		// 预占留着就是白占一个月的额度。
		OnFailed: releaseUsage,
	})

	// 后台循环（保留清理/审计归档/存储生命周期）依赖控制面全租户视图，仅 PostgreSQL 启用。
	if !stores.isSQLite() {
		startBackgroundLoops(ctx, stores, objects, auditStore, stdLogger)
	}

	// worker 进程单独暴露指标（影子分歧等计数在 worker 进程内自增；
	// api 的 /metrics 只反映 api 进程自身 expvar，看不到 worker 侧取值）。
	// 默认关闭；显式设置 PPTS_WORKER_METRICS_ADDR（如 127.0.0.1:8081）才监听，
	// 与 /metrics 的唯一准入一致：PPTS_METRICS_TOKEN 的 Bearer（fail-closed）。
	if addr := config.String("PPTS_WORKER_METRICS_ADDR", ""); addr != "" {
		go serveWorkerMetrics(addr, stdLogger)
	}

	// 任务级并发：单进程内跑 N 个执行循环（领取已加进程内互斥，处理仍并发）。
	// 默认 2：让「一键成稿」与「配音」等任务可并行，而不是排队；SQLite 单写者可调小到 1。
	workerConcurrency := config.Int("PPTS_WORKER_CONCURRENCY", 2)
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
		config.Duration("PPTS_QUEUE_BACKLOG_INTERVAL", 30*time.Second), stdLogger).Run(runCtx)
	go observability.NewQueueDepthReporter(stores.jobs, tenantID,
		config.Duration("PPTS_QUEUE_DEPTH_INTERVAL", 30*time.Second), stdLogger).Run(runCtx)
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
	sweeper := retention.NewSweeper(retention.NewPGStore(stores.pg), objects, config.Duration("PPTS_UPLOAD_ABANDON_TTL", 24*time.Hour), stdLogger).
		WithQuotaReservationTTL(config.Duration("PPTS_QUOTA_RESERVATION_TTL", 24*time.Hour)).
		// 孤儿扫描默认开启但**只报告**：删对象不可逆，而"无引用"的依据是"归属表里查不到引用行"，
		// 漏认一种引用关系就会删掉在用对象。人工核对审计里的 orphan.detected 后再开 PPTS_ORPHAN_DELETE。
		WithOrphanScan(config.Duration("PPTS_ORPHAN_GRACE", 24*time.Hour), config.Bool("PPTS_ORPHAN_DELETE", false)).
		// 终态任务保留清理（P3-6）：调度函数 ppts_claim_next_job 全表扫（0013 的 USING(true)
		// 策略使函数内 RLS 放开），终态行只增不减会线性拖慢每次轮询——基线见
		// internal/pipeline/growth_pg_test.go：16 万行时空转心跳 29.5ms。30 天后任务中心
		// 也不再展示这么久远的历史；要更久就调大 TTL。
		WithJobRetention(config.Duration("PPTS_JOB_RETENTION_TTL", 30*24*time.Hour)).
		WithAuditor(auditStore)
	go runSweeper(ctx, sweeper, config.Duration("PPTS_RETENTION_INTERVAL", time.Hour))

	if retDays := config.Int("PPTS_AUDIT_RETENTION_DAYS", 365); retDays > 0 {
		archiver := audit.NewArchiver(auditStore, retention.NewPGStore(stores.pg), objects, stdLogger)
		go runAuditArchiver(ctx, archiver, time.Duration(retDays)*24*time.Hour,
			config.Duration("PPTS_AUDIT_ARCHIVE_INTERVAL", time.Hour))
	}

	storageSyncer := storagelifecycle.NewSyncer(stores.tenant, objects, stdLogger)
	go runStorageLifecycleSyncer(ctx, storageSyncer, config.Duration("PPTS_STORAGE_LIFECYCLE_INTERVAL", 6*time.Hour))
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

// subtitleFontName 返回字幕烧录锁定的字体族名。
func subtitleFontName() string {
	if v := config.String("PPTS_SUBTITLE_FONT_NAME", ""); v != "" {
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
	dir := config.String("PPTS_ANIMATION_ASSET_DIR", "")
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
		// 同一个来源同时提供元素几何与焦点框：两者都来自 extracted document 的
		// 同一份形状树，分成两个适配器只会让"几何读了、框没读"这种半接状态出现。
		geom := app.NewDocumentGeometry(objects)
		assembler = assembler.WithGeometry(geom).WithFocus(geom)
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

// attachGateway 接入模型网关（G3 可视化配置）：DB 有配置时优先，否则回退 env 供应商。
// 加密密钥未配置或 DB 不可用时静默回退 env，不阻断 worker 启动。
func attachGateway(ctx context.Context, logger *slog.Logger, stores *storeSet, draft *app.ScriptDraftHandler, narr *app.NarrationHandler, envPolisher llm.TextRewriter, envTTS tts.TTSProvider, prefill *app.AnimationPrefillHandler) {
	cipher, err := gateway.CipherFromEnv()
	if err != nil {
		logger.Info("model gateway disabled (no AES key); using env providers")
		// 没有网关时仍然要接上这一层：任务点名了模型就必须失败，而不是用 env 供应商
		// 合成完再报成功。否则「界面选了 A、实际是 B」在最常见的私有化部署里依然成立。
		narr.WithTenantProvider(func(_ context.Context, tenantID, model string) (tts.TTSProvider, error) {
			if model != "" {
				return nil, fmt.Errorf("worker: model gateway disabled (no gateway AES key), cannot honor requested model %q", model)
			}
			if envTTS == nil {
				return nil, errors.New("worker: no TTS provider configured (env or gateway)")
			}
			logger.Info("narration tts resolved", "source", "env", "tenant", tenantID,
				"provider", config.String("PPTS_TTS_PROVIDER", ""))
			return envTTS, nil
		})
		// 网关不可用时预填仍可用：退到 env 供应商（如果有）。
		// 留着这条是因为「没有密钥」不该等于「预填永远走规则」——本地与私有化部署
		// 本来就只用环境变量配模型，那条路径必须一样能问模型。
		prefill.WithCompleter(func(context.Context, string) (bind.ModelCompleter, error) {
			if c, ok := envPolisher.(bind.ModelCompleter); ok && c != nil {
				return c, nil
			}
			return nil, errors.New("worker: no LLM provider configured")
		})
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
	narr.WithTenantProvider(func(ctx context.Context, tenantID, model string) (tts.TTSProvider, error) {
		// 任务点名了模型：必须按名解析，解析不到直接让任务失败。
		// 这里刻意不回退 env 供应商——那会让界面上的模型下拉成为装饰：
		// 用户选了 A、听到的是 env 里的 B，而任务状态仍是成功。
		if model != "" {
			cfg, err := gateway.ResolveModel(ctx, store, tenantID, gateway.KindTTS, model)
			if err != nil {
				return nil, err
			}
			logTTSResolution(logger, "requested-model", tenantID, cfg)
			return providers.TTS(cfg), nil
		}
		cfg, err := store.Resolve(ctx, tenantID, gateway.KindTTS)
		if err == nil {
			logTTSResolution(logger, "default-gateway", tenantID, cfg)
			return providers.TTS(cfg), nil
		}
		// 只有"本租户（含平台回退）没有任何可用默认网关"才允许回退 env。
		// 其它错误（解密失败、DB 故障）必须原样上抛：把它们当成"没配"同样会静默换音色。
		if !errors.Is(err, gateway.ErrNotFound) {
			return nil, err
		}
		// Resolve 的 ErrNotFound 同时覆盖"从没配过"和"配了但没设默认"，两者后果天差地别，
		// 所以这里必须再查一次列表才能分开。这一步不便宜，但只在回退路径上发生。
		rows, lerr := store.List(ctx, tenantID, gateway.KindTTS)
		if lerr != nil {
			return nil, fmt.Errorf("worker: 读取租户 %s 的 TTS 网关列表失败，无法判断是否漏设默认：%w", tenantID, lerr)
		}
		provider, ferr := ttsFallback(tenantID, rows, envTTS)
		if ferr != nil {
			return nil, ferr
		}
		// 回退成功也留一条 Warn：这是"界面里配了、实际没用上"的唯一线索。
		logger.Warn("narration tts resolved", "source", "env", "tenant", tenantID,
			"provider", config.String("PPTS_TTS_PROVIDER", ""))
		return provider, nil
	})
	// 预填与成稿共用同一套租户级 LLM 配置：用户在网关里换模型，
	// 预填跟着换，不需要第二个开关。解析不到就退回 env 供应商。
	prefill.WithCompleter(func(ctx context.Context, tenantID string) (bind.ModelCompleter, error) {
		if cfg, err := store.Resolve(ctx, tenantID, gateway.KindLLM); err == nil {
			if p, perr := providers.LLM(cfg); perr == nil && p != nil {
				return p, nil
			}
		}
		if c, ok := envPolisher.(bind.ModelCompleter); ok && c != nil {
			return c, nil
		}
		return nil, errors.New("worker: no LLM provider configured (env or gateway)")
	})
	logger.Info("model gateway attached", "ttl", "30s")
}

// logTTSResolution 记录本次配音任务最终用了哪个 TTS 来源。
//
// 存在的理由：这条路径此前一行日志都没有，"用了租户网关"与"掉了 env 兜底"在日志里
// 完全同形。真机排查时只能靠推理（去猜有没有标默认），代价远高于一行结构化日志。
// 只记名字与模型，绝不记 APIKey。
func logTTSResolution(logger *slog.Logger, source, tenantID string, cfg *gateway.Config) {
	logger.Info("narration tts resolved",
		"source", source, "tenant", tenantID,
		"gateway", cfg.Name, "model", cfg.Model, "voice", cfg.Voice)
}

// ttsFallback 决定"解析不到默认 TTS 网关"时该怎么走。
//
// 三种情形对用户含义完全不同，不能共用一条静默回退：
//   - 本租户没有任何 TTS 行：纯 env 部署，回退是对的；
//   - 有行但都不处于"启用 + 默认"：用户在界面里配过、还点过测试连接（那只查指定
//     名字那一行，与默认无关），但任务根本不会用它。此时回退 env（大概率是 fake）
//     会产出静音成片且状态成功——这是假成功，必须失败并指出去哪儿补；
//   - 既没有行也没有 env：本来就没配。
func ttsFallback(tenantID string, rows []*gateway.Gateway, envProvider tts.TTSProvider) (tts.TTSProvider, error) {
	if len(rows) > 0 {
		names := make([]string, 0, len(rows))
		for _, gw := range rows {
			if gw == nil {
				continue
			}
			label := gw.Name
			if !gw.Enabled {
				label += "(已停用)"
			}
			names = append(names, label)
		}
		return nil, fmt.Errorf(
			"worker: 租户 %s 有 %d 条 TTS 网关（%s），但没有一条同时处于「启用 + 默认」状态；请到「设置 → 模型服务」把要用的那条设为默认。不会回退到环境变量供应商——那会用另一套音色静默合成，而界面上的连通性测试依然是通过的",
			tenantID, len(rows), strings.Join(names, ", "))
	}
	if envProvider == nil {
		return nil, errors.New("worker: no TTS provider configured (env or gateway)")
	}
	return envProvider, nil
}

// requireTTSProvider 校验启动期至少有一条 TTS 配置路径可用。
//
// 为什么不能要求"必须有 env 供应商"：模型网关是产品的主路径（用户在 Web 里配），
// 它与 PPTS_TTS_PROVIDER 互不相干。纯网关部署若被这个变量拦住，症状是 worker 起不来，
// 而报错指向一个用户根本没听说过的环境变量——比"没配音"更难查。
func requireTTSProvider(envProvider tts.TTSProvider) error {
	if envProvider != nil {
		return nil
	}
	if _, err := gateway.CipherFromEnv(); err != nil {
		return errors.New("worker: no TTS provider configured: set PPTS_TTS_PROVIDER=siliconflow with PPTS_TTS_API_KEY, or configure a model gateway (requires PPTS_GATEWAY_AES_KEY_BASE64)")
	}
	return nil
}

// ttsProviderFromEnv 按 PPTS_TTS_PROVIDER 构建 TTS 供应商。
// siliconflow 需要 PPTS_TTS_BASE_URL / PPTS_TTS_API_KEY / PPTS_TTS_MODEL / PPTS_TTS_VOICE。
//
// 空值返回 (nil, nil)：那表示"不配 env 供应商"，而不是"配置缺失"——租户可以在 Web 里
// 配模型网关（DB 行），这条路不需要本变量。是否可接受由启动期结合网关可用性判定。
// 拼错 / 未知值仍然启动失败，绝不静默回落 fake：静音 WAV 与"合成成功"在 UI 上不可
// 区分，属于典型的假成功。fake 只能显式请求。
func ttsProviderFromEnv(logger *slog.Logger) (tts.TTSProvider, error) {
	switch provider := config.String("PPTS_TTS_PROVIDER", ""); provider {
	case "":
		return nil, nil
	case "siliconflow":
		return tts.NewSiliconFlowProvider(tts.SiliconFlowConfig{
			BaseURL: config.String("PPTS_TTS_BASE_URL", ""),
			APIKey:  config.String("PPTS_TTS_API_KEY", ""),
			Model:   config.String("PPTS_TTS_MODEL", ""),
			Voice:   config.String("PPTS_TTS_VOICE", ""),
		}), nil
	case "fake":
		// 这条日志说的是「env 回退供应商已就绪」，不是「本次任务会用 fake」。
		// 运行时先按租户→平台解析 DB 网关（logTTSResolution 会打 source=gateway），
		// 只有解析不到时才落到这条 env 回退。写成 "fake provider active" 会让运维
		// 误以为平台共享的 TTS 网关没生效，反复去查一个并不存在的问题。
		logger.Warn("TTS env fallback provider is fake: narration is SILENT only when no tenant/platform gateway resolves; not suitable for any non-development use",
			"hint", "runtime prefers a model gateway configured in Settings > Model services; otherwise set PPTS_TTS_PROVIDER=siliconflow with PPTS_TTS_API_KEY")
		observability.SetTTSFakeProviderActive(true)
		return tts.NewFakeProvider(), nil
	default:
		return nil, fmt.Errorf("unsupported PPTS_TTS_PROVIDER=%q (supported: fake, siliconflow); refusing to run because failure to configure TTS must not silently produce silent audio", provider)
	}
}

// animationMinutesKind 返回任务所属账期的动画计量种类。
//
// 账期取**任务创建时间**：预占发生在受理时（api），结算与释放可能发生在下一个账期
// （月末提交、月初跑完）。两边都按创建时间算账期，才指向同一行额度。
func animationMinutesKind(job *pipeline.Job) usage.Kind {
	created := job.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return usage.KindAnimationMinutes.Period(usage.MonthPeriod(created))
}
