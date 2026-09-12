// Package upload 定义授权直传会话（G1-1，V4.0 §11.1/§13.1）。
//
// CreateUpload 只签发受限对象键与预签名写链接，不落业务事实；
// CompleteUpload 校验大小/哈希/租户所有权后才创建源版本与解析任务。
// 会话状态（pending/completed/aborted）持久化，使重复的 CompleteUpload
// 幂等返回同一源版本与任务，Abort 后不可再完成。
package upload

import (
	"context"
	"errors"
	"time"
)

// State 上传会话状态。
type State string

const (
	StatePending   State = "pending"
	StateCompleted State = "completed"
	StateAborted   State = "aborted"
)

// UploadSession 是一次授权直传会话（源自数据库行）。
type UploadSession struct {
	ID                string
	TenantID          string
	ProjectID         string
	Filename          string
	ContentType       string
	SizeBytes         int64
	DeleteSourceAfter bool
	State             State
	ObjectKey         string
	SourceRevisionID  string
	JobID             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewUpload 创建会话的输入（ID 与 ObjectKey 由服务端分配）。
type NewUpload struct {
	ID                string
	TenantID          string
	ProjectID         string
	Filename          string
	ContentType       string
	ObjectKey         string
	SizeBytes         int64
	DeleteSourceAfter bool
}

// 错误哨兵：供上层（API/用例）分类映射 Connect 错误码。
var (
	// ErrNotFound 表示会话不存在或越权。
	ErrNotFound = errors.New("upload: session not found")
	// ErrAlreadyAborted 表示会话已中止，不能完成。
	ErrAlreadyAborted = errors.New("upload: session already aborted")
	// ErrAlreadyCompleted 表示会话已完成，不能再次完成/中止。
	ErrAlreadyCompleted = errors.New("upload: session already completed")
	// ErrStateConflict 表示状态转换冲突（并发完成等）。
	ErrStateConflict = errors.New("upload: state conflict")
)

// Store 上传会话存储端口。所有方法都要求显式 tenant_id。
type Store interface {
	Create(ctx context.Context, in NewUpload) (*UploadSession, error)
	Get(ctx context.Context, tenantID, uploadID string) (*UploadSession, error)
	// Complete 仅允许 pending→completed 原子迁移并回填结果；
	// 已 completed 幂等返回现有会话，已 aborted 返回 ErrAlreadyAborted。
	Complete(ctx context.Context, tenantID, uploadID, sourceRevisionID, jobID string) (*UploadSession, error)
	// Abort 仅允许 pending→aborted；已 aborted 幂等返回，已 completed 返回 ErrAlreadyCompleted。
	Abort(ctx context.Context, tenantID, uploadID string) (*UploadSession, error)
}
