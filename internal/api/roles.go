package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/membership"
)

// roleRank 返回角色的权限等级（越大越权）。未知角色视为无权限。
func roleRank(r membership.Role) int {
	switch r {
	case membership.RoleViewer:
		return 0
	case membership.RoleReviewer:
		return 1
	case membership.RoleEditor:
		return 2
	case membership.RoleAdmin:
		return 3
	case membership.RoleOwner:
		return 4
	default:
		return -1
	}
}

// requireRole 校验当前身份在租户内是否达到最低角色。
//
// reader 为 nil 时视为"未配置角色门禁"（开发/私有化部署），放行——保持向后兼容；
// 生产接入 `tenant_members` 后，缺失成员或角色不足一律拒绝。
func requireRole(ctx context.Context, reader membership.Reader, min membership.Role) error {
	if reader == nil {
		return nil
	}
	p, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	role, err := reader.GetRole(ctx, p.TenantID, p.UserID)
	if errors.Is(err, membership.ErrNotFound) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("no role granted in tenant"))
	}
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if roleRank(role) < roleRank(min) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("requires role %s or higher (got %s)", min, role))
	}
	return nil
}
