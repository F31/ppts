package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// projectAccessUser 返回用于**列表类**项目 ACL 过滤的用户 ID（#96 方案 A）。
//
// 分层语义：
//   - 租户 admin/owner → 返回空串（store SQL 中 `$1 = ''` 表示不过滤），可列出租户内全部项目；
//     列表属浏览性操作，不逐条审计。
//   - 其余成员 → 返回真实 userID，store 层仅返回 owner_user 或协作者项目。
//   - members 为 nil（测试桩/无成员服务）时不做旁路，退化为严格协作者模型。
func projectAccessUser(ctx context.Context, members membership.Reader) (userID string, override bool) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return "", false
	}
	if isTenantAdmin(ctx, members) {
		return "", true
	}
	return p.UserID, false
}

// isTenantAdmin 报告当前 principal 是否为租户 admin/owner（管理旁路资格）。
func isTenantAdmin(ctx context.Context, members membership.Reader) bool {
	p, ok := PrincipalFromContext(ctx)
	if !ok || members == nil {
		return false
	}
	role, err := members.GetRole(ctx, p.TenantID, p.UserID)
	return err == nil && roleRank(role) >= roleRank(membership.RoleAdmin)
}

// resolveProject 解析**单项目**访问，返回项目与是否走了管理旁路（供审计）。
//
// 顺序（避免把 owner 访问自己项目误记为越权审计）：
//  1. 按协作者模型严格查询（owner_user 或 project_collaborators）——命中即正常访问；
//  2. 未命中且当前身份为租户 admin/owner——回退管理旁路（override=true）；
//  3. 其余——返回 ErrProjectNotFound（404 防枚举）。
func resolveProject(ctx context.Context, projects project.ProjectStore, members membership.Reader, tenantID, userID, projectID string) (*project.Project, bool, error) {
	p, err := projects.GetProject(ctx, tenantID, userID, projectID)
	if err == nil {
		return p, false, nil
	}
	if !errors.Is(err, project.ErrProjectNotFound) {
		return nil, false, err
	}
	if !isTenantAdmin(ctx, members) {
		return nil, false, project.ErrProjectNotFound
	}
	p, err = projects.GetProject(ctx, tenantID, "", projectID)
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
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
//   - owner/协作者 → 放行（无审计）。
//   - 租户 admin/owner 但非项目成员 → 管理旁路放行 + 记 `project.admin_override_access` 审计。
//   - 其余 → 404（防枚举项目存在性）。
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
	_, override, err := resolveProject(r.Context(), projects, members, principal.TenantID, principal.UserID, projectID)
	if err != nil {
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
