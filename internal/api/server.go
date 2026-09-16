package api

import (
	"context"
	"expvar"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/public"
	"github.com/F31/ppts/internal/upload"
	"github.com/jackc/pgx/v5/pgxpool"
)

// handlerOpts 为所有 Connect handler 注入 RPC span（G3-8 OTel；未配置导出器时 no-op）。
var handlerOpts = []connect.HandlerOption{connect.WithInterceptors(tracingInterceptor())}

func tracingInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx, span := observability.Tracer("ppts.api").Start(ctx, req.Spec().Procedure)
			defer span.End()
			return next(ctx, req)
		}
	}
}

// Options 是可选横切依赖（G3-2 配额预占、G3-9 租户用量/策略）。
// tenant_id 始终由服务端从可信身份推导，客户端传入值不作授权依据。
type Options struct {
	Quota         QuotaManager
	Usage         TenantUsageReader
	Policy        TenantPolicyReader
	Audit         audit.Store
	Members       membership.Store
	Lifecycle     TenantLifecycle
	Storage       TenantStorageReader
	Archive       TenantArchiveReader
	TenantStatus  TenantStatusChecker
	Auth          Authenticator
	DevHeaders    bool
	Pronunciation pronunciation.Store
	Gateway       gateway.StoreResolver
}

// NewHandler builds the HTTP surface. Health checks intentionally bypass auth;
// all RPC routes are protected by AuthMiddlewareWithOptions. When objects is the local
// backend, signed object endpoints under /ppts/object serve direct reads/writes
// for local:// presigned URLs (single-node development).
func NewHandler(projects project.ProjectStore, uploads upload.Store, scripts narration.Store, jobs JobStore, artifacts artifact.Store, objects objectstore.ObjectStore, pool *pgxpool.Pool, opts ...Options) http.Handler {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /debug/vars", expvar.Handler())
	allowDevHeaders := opt.Auth == nil || opt.DevHeaders
	auth := func(handler http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(handler, AuthOptions{TenantStatus: opt.TenantStatus, Authenticator: opt.Auth, AllowDevHeaders: allowDevHeaders})
	}
	path, handler := pptsv1connect.NewProjectServiceHandler(NewProjectService(projects, objects, opt.Members), handlerOpts...)
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewUploadServiceHandler(NewUploadService(app.NewUploadService(uploads, projects, jobs, objects), objects, opt.Members), handlerOpts...)
	mux.Handle(path, auth(handler))
	// M3 ⑥：无备注页讲稿来源存储（pool 可用时启用；nil 时端点返回 feature_disabled）。
	var scriptSources app.ScriptSourceStore
	if pool != nil {
		scriptSources = app.NewScriptSourceStore(pool)
	}
	path, handler = pptsv1connect.NewScriptServiceHandler(NewScriptService(scripts, jobs, opt.Members, scriptSources), handlerOpts...)
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewNarrationServiceHandler(NewNarrationGenerationService(scripts, jobs, opt.Quota, opt.Policy, opt.Members), handlerOpts...)
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewExportServiceHandler(NewExportService(jobs, artifacts, objects, opt.Members), handlerOpts...)
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewPlaybackServiceHandler(NewPlaybackService(jobs, objects), handlerOpts...)
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewJobServiceHandler(NewJobService(jobs, opt.Quota, opt.Audit, opt.Members), handlerOpts...)
	mux.Handle(path, auth(handler))
	if opt.Usage != nil && opt.Policy != nil {
		path, handler = pptsv1connect.NewTenantServiceHandler(NewTenantService(opt.Usage, opt.Policy, opt.Members, opt.Audit, opt.Lifecycle, objects, opt.Storage, opt.Archive), handlerOpts...)
		mux.Handle(path, auth(handler))
	}
	if opt.Pronunciation != nil {
		NewPronunciationHandler(opt.Pronunciation).Register(mux, auth)
	}
	// 网关路由始终注册：未配置 AES 密钥时 store 为 nil，handler 返回明确 503 feature_disabled，
	// 避免此前漏挂导致的静默 404。
	NewGatewayHandler(opt.Gateway, opt.Members, opt.Audit).Register(mux, auth)
	// 公开区路由（匿名只读 + 受保护写/审核）；B3 播放清单依赖 jobs。
	// 注：此前提交漏挂此调用，导致公开区/B3 端点从未生效，本轮补回。
	if pool != nil {
		registerPublicRoutes(mux, public.NewPGStore(pool), objects, opt.Members, jobs, auth)
	}
	// 核心创作辅助路由（待确认稿计数 + stale 信号），供生成面板前置检查（C-5）。
	registerNarrationRoutes(mux, scripts, opt.Members, auth)
	// 成品列表路由（按项目列出产物，前端按快照聚合），供成品与版本页展示与下载（B3-M1）。
	registerArtifactRoutes(mux, artifacts, opt.Members, auth)
	// 核心创作编辑器辅助路由（真实渲染缩略图/预览 + 无备注页来源，B2 M2/M3）。
	registerEditorRoutes(mux, jobs, objects, scriptSources, auth)
	// 任务详情辅助路由（范围/受影响页/输入版本/执行步骤/traceId，B4-M6a）。
	registerJobDetailRoutes(mux, jobs, auth)
	// 任务列表筛选/排序/翻页（阶段筛选 + 多键排序 + keyset 游标，B4-M6b）。
	registerJobListRoutes(mux, jobs, auth)
	if parser, ok := objects.(signedURLParser); ok {
		objHandler := &signedObjectHandler{objects: objects, parser: parser}
		mux.HandleFunc("GET /ppts/object/{key...}", func(w http.ResponseWriter, r *http.Request) {
			objHandler.serve(objectstore.OpRead, w, r)
		})
		mux.HandleFunc("PUT /ppts/object/{key...}", func(w http.ResponseWriter, r *http.Request) {
			objHandler.serve(objectstore.OpWrite, w, r)
		})
		mux.HandleFunc("DELETE /ppts/object/{key...}", func(w http.ResponseWriter, r *http.Request) {
			objHandler.serve(objectstore.OpDelete, w, r)
		})
	}
	// 可选 SPA 兜底：由外部反代（nginx/Caddy）或前端 dev server 处理，
	// 单二进制部署时可通过 nginx location / { try_files $uri $uri /index.html; } 实现。
	return mux
}

// spaFallbackHandler 在 PPTS_WEB_ROOT 指向前端构建目录时，为 SPA 提供 history 路由兜底：
// 真实静态资源（带扩展名）命中则返回、未命中 404；其余 GET 请求返回 index.html 交由客户端路由接管。
// 系统/API/对象存储前缀一律放行 404，避免与已注册路由冲突。
func spaFallbackHandler(webRoot string) http.HandlerFunc {
	fileServer := http.FileServer(http.Dir(webRoot))
	indexHTML := filepath.Join(webRoot, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/ppts/object") || strings.HasPrefix(p, "/healthz") || strings.HasPrefix(p, "/debug") {
			http.NotFound(w, r)
			return
		}
		if ext := filepath.Ext(p); ext != "" {
			f := filepath.Join(webRoot, filepath.Clean(p))
			if fi, err := os.Stat(f); err == nil && !fi.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFile(w, r, indexHTML)
	}
}
