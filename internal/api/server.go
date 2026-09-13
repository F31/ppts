package api

import (
	"expvar"
	"net/http"

	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/upload"
)

// Options 是可选横切依赖（G3-2 配额预占、G3-9 租户用量/策略）。
// tenant_id 始终由服务端从可信身份推导，客户端传入值不作授权依据。
type Options struct {
	Quota        QuotaManager
	Usage        TenantUsageReader
	Policy       TenantPolicyReader
	Audit        audit.Store
	Members      membership.Store
	Lifecycle    TenantLifecycle
	TenantStatus TenantStatusChecker
}

// NewHandler builds the HTTP surface. Health checks intentionally bypass auth;
// all RPC routes are protected by AuthMiddleware. When objects is the local
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
	auth := func(handler http.Handler) http.Handler { return AuthMiddleware(handler, opt.TenantStatus) }
	path, handler := pptsv1connect.NewProjectServiceHandler(NewProjectService(projects, objects, opt.Members))
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewUploadServiceHandler(NewUploadService(app.NewUploadService(uploads, projects, jobs, objects), objects, opt.Members))
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewScriptServiceHandler(NewScriptService(scripts, jobs, opt.Members))
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewNarrationServiceHandler(NewNarrationGenerationService(scripts, jobs, opt.Quota, opt.Policy, opt.Members))
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewExportServiceHandler(NewExportService(jobs, artifacts, objects, opt.Members))
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewPlaybackServiceHandler(NewPlaybackService(jobs, objects))
	mux.Handle(path, auth(handler))
	path, handler = pptsv1connect.NewJobServiceHandler(NewJobService(jobs, opt.Quota, opt.Audit, opt.Members))
	mux.Handle(path, auth(handler))
	if opt.Usage != nil && opt.Policy != nil {
		path, handler = pptsv1connect.NewTenantServiceHandler(NewTenantService(opt.Usage, opt.Policy, opt.Members, opt.Audit, opt.Lifecycle, objects))
		mux.Handle(path, auth(handler))
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
