package api

import (
	"errors"
	"net/http"

	"github.com/F31/ppts/internal/project"
)

// requireProjectAccess 校验当前 principal 对指定项目的访问权。
// 非 owner 且非协作者返回 404（防枚举项目存在性）；未认证返回 401。
// 返回项目 ID 与 true，失败时已写响应。
func requireProjectAccess(w http.ResponseWriter, r *http.Request, projects project.ProjectStore) (string, bool) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	projectID := r.PathValue("pid")
	if projectID == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return "", false
	}
	// GetProject 现已在 store 层执行协作者可见性过滤；非协作者返回 ErrProjectNotFound → 404。
	if _, err := projects.GetProject(r.Context(), principal.TenantID, principal.UserID, projectID); err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return "", false
	}
	return projectID, true
}
