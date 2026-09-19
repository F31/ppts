package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// projectAccessUser 返回用于项目 ACL 过滤的用户 ID（#96 方案 A）。
//
// 分层语义：
//   - 租户 admin/owner 拥有**管理旁路**：返回空串（store 层 SQL 中 `$1 = ''` 表示不过滤），
//     override=true 供调用方记审计。管理员可查看/管理租户内全部项目。
//   - 其余成员走**协作者模型**：返回真实 userID，store 层仅返回 owner_user 或协作者项目。
//   - members 为 nil（测试桩/无成员服务）时不做旁路，退化为严格协作者模型。
func projectAccessUser(ctx context.Context, members membership.Reader) (userID string, override bool) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return "", false
	}
	if members != nil {
		if role, err := members.GetRole(ctx, p.TenantID, p.UserID); err == nil && roleRank(role) >= roleRank(membership.RoleAdmin) {
			return "", true
		}
	}
	return p.UserID, false
}

// recordAdminOverride 记录一次管理员旁路访问审计（recorder 为 nil 时静默跳过）。
func recordAdminOverride(ctx context.Context, recorder audit.Recorder, action, projectID string, meta map[string]any) {
	if recorder == nil {
		return
	}
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return
	}
	_ = recorder.Record(ctx, audit.Event{
		TenantID: p.TenantID, ActorUser: p.UserID, Action: action,
		ResourceType: "project", ResourceID: projectID, Metadata: meta,
	})
}

// requireProjectAccess 校验当前 principal 对指定项目的访问权（方案 A）。
//
//   - 未认证 → 401；缺 project_id → 400。
//   - 租户 admin/owner → 管理旁路放行，并记 `project.admin_override_access` 审计。
//   - 其余成员 → owner/协作者放行；非协作者返回 404（防枚举项目存在性）。
//
// 返回项目 ID 与 true，失败时已写响应。
func requireProjectAccess(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) (string, bool) {
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
	userID, override := projectAccessUser(r.Context(), members)
	if _, err := projects.GetProject(r.Context(), principal.TenantID, userID, projectID); err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return "", false
	}
	if override {
		recordAdminOverride(r.Context(), recorder, "project.admin_override_access", projectID, map[string]any{"surface": "http"})
	}
	return projectID, true
}
