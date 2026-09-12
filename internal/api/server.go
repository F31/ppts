package api

import (
	"net/http"

	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/upload"
)

// NewHandler builds the HTTP surface. Health checks intentionally bypass auth;
// all RPC routes are protected by AuthMiddleware. When objects is the local
// backend, signed object endpoints under /ppts/object serve direct reads/writes
// for local:// presigned URLs (single-node development).
func NewHandler(projects project.ProjectStore, uploads upload.Store, scripts narration.Store, jobs JobCreator, artifacts artifact.Store, objects objectstore.ObjectStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	path, handler := pptsv1connect.NewProjectServiceHandler(NewProjectService(projects, objects))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewUploadServiceHandler(NewUploadService(app.NewUploadService(uploads, projects, jobs, objects), objects))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewScriptServiceHandler(NewScriptService(scripts, jobs))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewNarrationServiceHandler(NewNarrationGenerationService(scripts, jobs))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewExportServiceHandler(NewExportService(jobs, artifacts, objects))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewPlaybackServiceHandler(NewPlaybackService(jobs, objects))
	mux.Handle(path, AuthMiddleware(handler))
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
