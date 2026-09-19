package api

import (
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// registerArtifactRoutes 挂载成品列表原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET /projects/{pid}/artifacts：按项目列出全部产物（前端再按 snapshotHash 分组），供成品与版本页展示与下载（B3-M1）；
//   - GET /artifacts：跨项目成品库（owner 级），供 B5-M2 成品库页展示。
//
// 注意：/artifacts 此前 handler（globalArtifacts）已写但漏挂路由，请求落到 SPA 兜底（server.go 的 "/"）
// 拿到 index.html，前端 JSON 解析报 `Unexpected token '<', "<!doctype"`。新增任何 REST handler
// 必须同步在注册表挂载（见项目风险 R-10）。
func registerArtifactRoutes(mux *http.ServeMux, artifacts artifact.Store, members membership.Store, projects project.ProjectStore, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/artifacts", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicProjectArtifacts(w, r, artifacts, members, projects, recorder)
	})))
	mux.Handle("GET /artifacts", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalArtifacts(w, r, artifacts, members)
	})))
}

func publicProjectArtifacts(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store, projects project.ProjectStore, recorder audit.Recorder) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
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

// globalArtifacts 返回当前租户全部项目的成品（B5-M2 跨项目成品库）。
// 权限：owner（跨项目曝光，高于单项目 artifact.list 的 editor）；按创建时间倒序。
func globalArtifacts(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleOwner); err != nil {
		writeConnectError(w, err)
		return
	}
	list, err := artifacts.ListAll(r.Context(), principal.TenantID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id":           a.ID,
			"projectId":    a.ProjectID,
			"projectName":  a.ProjectName,
			"snapshotHash": a.SnapshotHash,
			"format":       string(a.Format),
			"sizeBytes":    a.SizeBytes,
			"durationMs":   a.DurationMS,
			"createdAt":    a.CreatedAt.Format(time.RFC3339),
			"downloadable": a.ObjectKey != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}
