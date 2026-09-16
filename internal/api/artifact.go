package api

import (
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/membership"
)

// registerArtifactRoutes 挂载成品列表原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC）：
// 按项目列出全部产物（前端再按 snapshotHash 分组），供成品与版本页展示与下载（B3-M1）。
func registerArtifactRoutes(mux *http.ServeMux, artifacts artifact.Store, members membership.Store, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/artifacts", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicProjectArtifacts(w, r, artifacts, members)
	})))
}

func publicProjectArtifacts(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	pid := r.PathValue("pid")
	list, err := artifacts.ListByProject(r.Context(), principal.TenantID, pid)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id":           a.ID,
			"snapshotHash": a.SnapshotHash,
			"format":       string(a.Format),
			"sizeBytes":    a.SizeBytes,
			// durationMs = 0 表示未知/未记录（0027 之前落库的历史行），前端显示「—」不伪造。
			"durationMs":   a.DurationMS,
			"createdAt":    a.CreatedAt.Format(time.RFC3339),
			"downloadable": a.ObjectKey != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}
