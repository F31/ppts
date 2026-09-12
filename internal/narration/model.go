package narration

import (
	"context"
	"errors"
	"time"
)

// ScriptMode 稿件模式（V4.0 §3.1）。
type ScriptMode string

const (
	ModeOriginal    ScriptMode = "original"     // 原文朗读
	ModePolish      ScriptMode = "polish"       // 润色讲解
	ModeAIGenerated ScriptMode = "ai_generated" // AI 生成讲解
)

// ScriptStatus 稿件状态。
type ScriptStatus string

const (
	StatusDraft    ScriptStatus = "draft"
	StatusApproved ScriptStatus = "approved"
	StatusLocked   ScriptStatus = "locked"
)

// Revision 是一份讲稿（每 (project, slide, language) 一份）的当前版本。
// 已审核/锁定稿不可被后台或再生成覆盖（V4.0 §3.1 优先级）。
type Revision struct {
	ID        string
	TenantID  string
	ProjectID string
	SlideID   string
	Language  string
	Mode      ScriptMode
	Status    ScriptStatus
	Revision  int64
	Segments  []*Segment
	UpdatedAt time.Time
}

// Segment 是可复用最小单位（语音生成与字幕共用）。
type Segment struct {
	SegmentID   string
	DisplayText string
	SpokenText  string
	SourceRefs  []string
	Status      ScriptStatus
}

// ErrNotFound 表示讲稿不存在。
var ErrNotFound = errors.New("narration: script not found")

// ErrLocked 表示目标已锁定，禁止修改。
var ErrLocked = errors.New("narration: script is locked")

// ErrConflict 表示 expected_revision 与最新版本不一致；Latest 携带最新版供对比。
// 禁止最后写入者静默覆盖（V4.0 §11.1）。
type ErrConflict struct {
	Latest *Revision
}

func (e *ErrConflict) Error() string { return "narration: revision conflict" }

// Store 讲稿存储端口。更新带 expected_revision；冲突返回 ErrConflict{Latest}。
type Store interface {
	// Get 取指定 slide 当前讲稿；不存在返回 ErrNotFound。
	Get(ctx context.Context, tenantID, projectID, slideID, language string) (*Revision, error)
	// Update 以 expected_revision 乐观并发更新内容；冲突返回 ErrConflict。
	Update(ctx context.Context, tenantID, projectID, slideID, language string, expected int64, segments []*Segment) (*Revision, error)
	// SetStatus 审批/锁定（仅 draft→approved→locked 单向；锁定后拒绝回退到 draft）。
	SetStatus(ctx context.Context, tenantID, projectID, slideID, language string, newStatus ScriptStatus) (*Revision, error)
	// EnsureExists 在编辑前创建草稿占位（幂等）。
	EnsureExists(ctx context.Context, tenantID, projectID, slideID, language string, mode ScriptMode) (*Revision, error)
}
