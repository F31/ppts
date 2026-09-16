// Package public 提供公开作品发布（V1.6 公开区 C-1）的存储与领域模型。
//
// 两类公开作品：
//   - KindFeatured：官方精选，由管理员发布即 approved（直接公开展示）；
//   - KindUser：用户作品，由用户发布为 pending，经管理员审核 approve 后公开。
//
// 只读接口（ListApproved/GetApprovedByPublicID）设计为匿名调用：调用方不经过 tenant.Run，
// 直接走 pgxpool 查询，由 RLS 的 publications_public_read 策略仅放行 status='approved'。
// 写接口均在租户 RLS 上下文（tenant.Run）内执行，行租户须匹配调用方租户。
package public

import (
	"context"
	"errors"
	"time"
)

// Kind 是公开作品分类。
type Kind string

const (
	KindFeatured Kind = "featured" // 官方精选：管理员发布
	KindUser     Kind = "user"     // 用户作品：用户发布，需审核
)

// Valid 判断是否为合法的公开作品分类。
func (k Kind) Valid() bool {
	switch k {
	case KindFeatured, KindUser:
		return true
	default:
		return false
	}
}

// Status 是公开作品的审核状态。
type Status string

const (
	StatusDraft     Status = "draft"
	StatusPending   Status = "pending"   // 用户已发布，待审核
	StatusApproved  Status = "approved"  // 已公开
	StatusRejected  Status = "rejected"  // 审核驳回
	StatusWithdrawn Status = "withdrawn" // 已撤回（立即失效，匿名不可读，B5-M3）
)

// Publication 是公开作品领域实体（对应 publications 表）。
type Publication struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	ProjectID      string    `json:"project_id"`
	Kind           Kind      `json:"kind"`
	Status         Status    `json:"status"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	CoverObjectKey string    `json:"cover_object_key,omitempty"`
	SortOrder      int       `json:"sort_order"`
	CreatedBy      string    `json:"created_by"`
	ReviewedBy     string    `json:"reviewed_by,omitempty"`
	ReviewedAt     time.Time `json:"reviewed_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	PublicID       string    `json:"public_id"`
	WithdrawnAt    time.Time `json:"withdrawn_at,omitempty"`
}

// NewPublication 新建公开作品的输入。
type NewPublication struct {
	ProjectID      string
	Kind           Kind
	Title          string
	Summary        string
	CoverObjectKey string
	CreatedBy      string
}

// ErrNotFound 表示公开作品不存在或越权。
var ErrNotFound = errors.New("publication: not found")

// Store 是公开作品存储端口。
type Store interface {
	// Publish 由用户在自己租户发布作品，初始状态为 pending（待审核）。
	Publish(ctx context.Context, tenantID string, in NewPublication) (*Publication, error)
	// Feature 由管理员在租户内发布官方精选，初始状态为 approved（直接公开）。
	Feature(ctx context.Context, tenantID string, in NewPublication) (*Publication, error)
	// ListApproved 匿名列出已批准作品；kind 为空表示两类合并。
	ListApproved(ctx context.Context, kind Kind, cursor string, pageSize int) ([]*Publication, string, error)
	// GetApprovedByPublicID 匿名获取单条已批准作品（按不可反推的 public_id）。B5-M3。
	GetApprovedByPublicID(ctx context.Context, publicID string) (*Publication, error)
	// ListByProject 列出某租户某项目下的全部发布（含未批准），供"我的发布"页使用。
	ListByProject(ctx context.Context, tenantID, projectID string) ([]*Publication, error)
	// Review 由管理员审核：approve=true 置 approved，否则置 rejected。
	Review(ctx context.Context, tenantID, id string, approve bool, reviewer string) (*Publication, error)
	// Recall 由 owner/admin 撤回已发布作品：置 withdrawn 立即失效（含匿名读）。B5-M3。
	Recall(ctx context.Context, tenantID, publicID, by string) (*Publication, error)
	// ListMine 列出某租户某用户的全部发布（含未批准），供"我的发布"页使用。
	ListMine(ctx context.Context, tenantID, createdBy string, status Status) ([]*Publication, error)
	// ListPending 列出某租户待审核的发布，供审核队列使用。
	ListPending(ctx context.Context, tenantID string, kind Kind) ([]*Publication, error)
	Delete(ctx context.Context, tenantID, id string) error
}
