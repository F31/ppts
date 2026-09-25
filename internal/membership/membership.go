// Package membership 提供租户成员与角色存储（G3-3 内核，V4.0 §12.3）。
//
// 角色模型 Owner/Admin/Editor/Reviewer/Viewer；tenant_id 由服务端从身份推导。
// 完整 OIDC 身份接入与授权矩阵见 G3-3 后续。
package membership

import (
	"context"
	"errors"
	"time"
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

// Member 是租户成员（含展示用档案字段，由 List 经 users/user_profiles 联表填充）。
type Member struct {
	UserID    string
	Role      Role
	CreatedAt time.Time // 加入租户时间（tenant_members.created_at）
	Email     string    // users.email（登录账号）
	Username  string    // user_profiles.username（展示用用户名，可空）
	FullName  string    // user_profiles.full_name（姓名）
	Gender    string    // user_profiles.gender
	BirthDate string    // user_profiles.birth_date，格式 YYYY-MM-DD（出生年月）
	Phone     string    // user_profiles.phone（电话）
}

// Profile 是可编辑的成员档案字段。
type Profile struct {
	Username  string
	FullName  string
	Gender    string
	BirthDate string
	Phone     string
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
	// SaveProfile 写入成员档案（user_profiles）。tenantID 是**必填**的归属约束而非可选元数据：
	// user_profiles 无租户列且不启用 RLS，若实现只按 user_id 定位，一个租户的 Admin 就能改写
	// 任意已知 userID（含其他租户用户）的档案（见 docs/架构审视与优化方案 R4）。
	// 目标用户不属于调用方租户时返回 ErrNotFound。
	SaveProfile(ctx context.Context, tenantID, userID string, p Profile) error
}
