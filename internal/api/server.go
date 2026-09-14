package api

import (
	"context"
	"expvar"
	"net/http"

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
	"github.com/F31/ppts/internal/upload"
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
func NewHandler(projects project.ProjectStore, uploads upload.Store, scripts narration.Store, jobs JobStore, artifacts artifact.Store, objects objectstore.ObjectStore, opts ...Options) http.Handler {
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
	path, handler = pptsv1connect.NewScriptServiceHandler(NewScriptService(scripts, jobs, opt.Members), handlerOpts...)
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
	if opt.Gateway != nil {
		NewGatewayHandler(opt.Gateway, opt.Members, opt.Audit).Register(mux, auth)
	}
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
	return mux
}
