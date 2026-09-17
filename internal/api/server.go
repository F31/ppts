package api

import (
	"context"
	"expvar"
	"io/fs"
	"net/http"
	"os"
	"path"
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
	// 邮箱自助注册（B5-M4）：JWT 签发/校验密钥与密码全局 pepper，均来自环境变量，不落库。
	JWTSecret      string
	PasswordPepper string
	// WebRoot 指向前端构建产物目录（vite build 输出）。非空时由 Go 直接托管静态资源与
	// SPA history 兜底 —— 单二进制部署无需 nginx。
	WebRoot string
	// WebFS 是内嵌的前端构建产物（go:embed，见 web 包），WebRoot 为空时作为兜底；
	// 两者皆空 = 维持旧行为，由外部反代托管前端。
	WebFS fs.FS
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
	// 邮箱自助注册（B5-M4）：注册/登录/能力探测端点。pool 为 nil 时不挂载（测试桩）。
	if pool != nil {
		registerAuthRoutes(mux, pool, opt.JWTSecret, opt.PasswordPepper)
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
	// SPA 兜底：WebRoot 或内嵌 WebFS 非空时由 Go 直接托管前端构建产物（单二进制部署，替代 nginx）。
	// "/" 是最宽泛的模式：Go 1.22+ ServeMux 让更具体的已注册路由优先，故不会遮蔽任何 API。
	switch {
	case opt.WebRoot != "":
		mux.Handle("/", spaFallbackHandler(os.DirFS(opt.WebRoot)))
	case opt.WebFS != nil:
		mux.Handle("/", spaFallbackHandler(opt.WebFS))
	}
	return mux
}

// hashedAssetPrefix 是 vite 构建输出的哈希资源目录：文件名内嵌内容哈希，可长期强缓存。
const hashedAssetPrefix = "/assets/"

// spaFallbackHandler 为 SPA 提供 history 路由兜底：
// 真实静态资源（带扩展名）命中则返回、未命中 404；其余 GET/HEAD 请求返回 index.html 交由客户端路由接管。
// 系统/API/对象存储前缀一律放行 404，避免与已注册路由冲突。
// HEAD 与 GET 同权（原 nginx 拓扑支持 HEAD，CDN/代理常用它探测静态资源）。
// fsys 可以是 os.DirFS(webRoot)（外部目录）或内嵌 embed.FS（单二进制分发）。
func spaFallbackHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/ppts/object") || strings.HasPrefix(p, "/healthz") || strings.HasPrefix(p, "/debug") {
			http.NotFound(w, r)
			return
		}
		if ext := path.Ext(p); ext != "" {
			// fs.ValidPath 要求无前导斜杠的相对路径。
			name := path.Join(strings.TrimPrefix(p, "/"))
			if fi, err := fs.Stat(fsys, name); err == nil && !fi.IsDir() {
				// 接替 nginx 原先的 expires 1y / immutable：只对 /assets/ 下的内容哈希文件生效，
				// 避免像旧 nginx 规则那样把 favicon.svg 这类非哈希资源也缓存一年。
				if strings.HasPrefix(p, hashedAssetPrefix) {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFileFS(w, r, fsys, "index.html")
	}
}
