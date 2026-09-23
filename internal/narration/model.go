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

// Revision 是一份讲稿（每 (project, source_revision, slide, language) 一份）的当前版本。
// 已审核/锁定稿不可被后台或再生成覆盖（V4.0 §3.1 优先级）。
type Revision struct {
	ID            string
	TenantID      string
	ProjectID     string
	SlideID       string
	Language      string
	Mode          ScriptMode
	Status        ScriptStatus
	Revision      int64
	AudioRevision int64 // 最近一次配音对应的脚本修订号；< Revision 表示配音可能过期（stale）
	// SourceRevisionNo 是讲稿所属的源版本（源 PPTX 版本号）。0 表示 legacy：
	// 该稿在引入"按版本隔离"之前生成，读路径按回退可见（见 Store 文档）。
	SourceRevisionNo int
	Segments         []*Segment
	UpdatedAt        time.Time
}

// Segment 是可复用最小单位（语音生成与字幕共用）。
type Segment struct {
	SegmentID   string
	DisplayText string
	SpokenText  string
	SourceRefs  []string
	// SourceAnchors contains structured provenance. Nil means "preserve existing"
	// when updating through user-facing APIs; an empty slice means explicitly no anchors.
	SourceAnchors []SourceAnchor
	Status        ScriptStatus
}

// SourceAnchor ties generated text back to structural or visual evidence.
type SourceAnchor struct {
	SlideID    string
	ShapeID    string
	Kind       string
	Raw        string
	Confidence float64
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
//
// 所有读写都以 sourceRevisionNo 定位「源版本」：slide_id 只在单个 PPTX 文件内唯一，
// 改版重传或不同文件同 id 时不同版本会共用 slide_id，必须靠源版本区分。
// sourceRevisionNo==0 表示 legacy（引入版本隔离之前的存量稿）：
//   - 写：落到 0（仅由未升级的调用方/迁移后未分叉时出现）；
//   - 读：先精确匹配该版本，缺失则回退到 legacy（保证存量稿在任意版本可见，直到某版本首次写入）。
type Store interface {
	// Get 取指定源版本下该 slide 的讲稿；不存在（含 legacy 回退后仍无）返回 ErrNotFound。
	Get(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string) (*Revision, error)
	// Update 以 expected_revision 乐观并发更新内容；冲突返回 ErrConflict。
	// 若该源版本尚无行但存在 legacy 行，会先从 legacy 分叉出一份再更新。
	Update(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, expected int64, segments []*Segment) (*Revision, error)
	// SetStatus 审批/锁定（draft→approved→locked；locked 可回到 approved 以重新编辑）。
	SetStatus(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, newStatus ScriptStatus) (*Revision, error)
	// EnsureExists 在编辑前创建草稿占位（幂等，按源版本）。
	EnsureExists(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, mode ScriptMode) (*Revision, error)
	// CountDraftSegments 返回项目内仍处于 draft 状态的讲稿分段总数（生成前置检查用，跨版本）。
	CountDraftSegments(ctx context.Context, tenantID, projectID string) (int, error)
	// MarkAudioRevision 回写某讲稿最近一次成功配音对应的脚本修订号（配音任务完成时调用）。
	MarkAudioRevision(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, slideID, language string, revision int64) error
	// ListByProject 返回项目下指定语言、指定源版本的讲稿（含 AudioRevision，供 stale 计算）。
	// sourceRevisionNo==0 时只返回 legacy 行；>0 时返回该版本行，并对缺少该版本行的 slide 回退 legacy。
	ListByProject(ctx context.Context, tenantID, projectID string, sourceRevisionNo int, language string) ([]*Revision, error)
}
