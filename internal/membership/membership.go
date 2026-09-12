// Package membership 提供租户成员与角色存储（G3-3 内核，V4.0 §12.3）。
//
// 角色模型 Owner/Admin/Editor/Reviewer/Viewer；tenant_id 由服务端从身份推导。
// 完整 OIDC 身份接入与授权矩阵见 G3-3 后续。
package membership

import (
	"context"
	"errors"
)

// Role 是租户成员角色。
type Role string

const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleEditor   Role = "editor"
	RoleReviewer Role = "reviewer"
	RoleViewer   Role = "viewer"
)

// Valid 判断角色是否在模型中。
func Valid(r Role) bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleEditor, RoleReviewer, RoleViewer:
		return true
	default:
		return false
	}
}

// ErrNotFound 表示成员不存在。
var ErrNotFound = errors.New("membership: member not found")

// Member 是租户成员。
type Member struct {
	UserID string
	Role   Role
}

// Reader 是只读成员能力（TenantService.Members/Roles 需要）。
type Reader interface {
	GetRole(ctx context.Context, tenantID, userID string) (Role, error)
	List(ctx context.Context, tenantID string) ([]Member, error)
}

// Store 是完整成员存储。
type Store interface {
	Reader
	SetRole(ctx context.Context, tenantID, userID string, role Role) error
	Remove(ctx context.Context, tenantID, userID string) error
}
