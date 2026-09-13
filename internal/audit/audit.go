// Package audit 提供租户隔离的审计日志存储（G3-4）。
//
// 审计事件仅追加；所有读写经租户事务（RLS），确保按租户可检索且不可跨租户读取。
package audit

import (
	"context"
	"errors"
	"time"
)

// ErrTenantRequired 表示记录审计事件时缺少租户。
var ErrTenantRequired = errors.New("audit: tenant_id is required")

// ErrActionRequired 表示记录审计事件时缺少动作。
var ErrActionRequired = errors.New("audit: action is required")

// Event 是一条审计事件。
type Event struct {
	ID           string
	TenantID     string
	ActorUser    string
	Action       string
	ResourceType string
	ResourceID   string
	Metadata     map[string]any
	CreatedAt    time.Time
}

// Recorder 记录审计事件（供业务组件依赖的最小能力）。
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// Filter 是审计查询条件。
type Filter struct {
	Action       string
	ResourceType string
	Since        time.Time // created_at >= since
	Before       time.Time // created_at < before（归档/到期清理用，排他）
	Limit        int
}

// Store 是审计存储。
type Store interface {
	Recorder
	List(ctx context.Context, tenantID string, filter Filter) ([]Event, error)
	// DeleteBefore 删除 before 之前（排他）的审计事件，返回删除行数。供保留/归档清理。
	DeleteBefore(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
