package api

import (
	"net/http"

	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// NewHandler builds the HTTP surface. Health checks intentionally bypass auth;
// all RPC routes are protected by AuthMiddleware.
func NewHandler(projects project.ProjectStore, scripts narration.Store, jobs JobCreator, artifacts artifact.Store, objects objectstore.ObjectStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	path, handler := pptsv1connect.NewProjectServiceHandler(NewProjectService(projects))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewScriptServiceHandler(NewScriptService(scripts))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewNarrationServiceHandler(NewNarrationGenerationService(scripts, jobs))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewExportServiceHandler(NewExportService(jobs, artifacts, objects))
	mux.Handle(path, AuthMiddleware(handler))
	path, handler = pptsv1connect.NewPlaybackServiceHandler(NewPlaybackService(objects))
	mux.Handle(path, AuthMiddleware(handler))
	return mux
}
