// Package artifact stores immutable export artifacts.
package artifact

import (
	"context"
	"errors"
	"time"
)

type Format string

const (
	FormatMP4        Format = "mp4"
	FormatSRT        Format = "srt"
	FormatVTT        Format = "vtt"
	FormatWebProject Format = "web_project"
)

type Artifact struct {
	ID        string
	TenantID  string
	ProjectID string
	// ProjectName 是跨项目成品库（GET /artifacts，B5-M2）所需的冗余展示字段：
	// 由 ListAll 经 LEFT JOIN projects 填充；Get/ListByProject 不填充（空串）。
	ProjectName  string
	SnapshotHash string
	Format       Format
	ObjectKey    string
	ContentHash  string
	SizeBytes    int64
	// DurationMS 是成品所绑定时间轴的实际时长（media.Timeline.DurationUS / 1000）；
	// 0 表示未知/未记录（历史行），界面显示「—」而不伪造（迁移 0027）。
	DurationMS int64
	// TimelineKey 是该次导出所用时间轴的对象键（迁移 0040）；空串表示历史行未知，
	// 成品库据此隐藏"预览"按钮（降级为仅下载）。与 ObjectKey 的关系：
	// ObjectKey 是成品文件本身（mp4/zip/字幕），TimelineKey 是生成它的时间轴。
	TimelineKey string
	// SourceRevisionNo 是该成品所源自的源版本号（由 narration 写入时间轴、export 落库，见 internal/app/export.go）；
	// 0 表示未知（历史行）。成品库据此与 SourceDisplayName 展示"PPT 名称 vN"。
	SourceRevisionNo int
	// SourceDisplayName 是该源版本的展示名（source_revisions.display_name）；空表示历史行未知，
	// 前端回退到 projectName。与 SourceRevisionNo 一并，避免依赖成品↔source_revisions 缺失外键。
	SourceDisplayName string
	CreatedAt         time.Time
}

type NewArtifact struct {
	ProjectID    string
	SnapshotHash string
	Format       Format
	ObjectKey    string
	ContentHash  string
	SizeBytes    int64
	// DurationMS 见 Artifact.DurationMS。
	DurationMS int64
	// TimelineKey 见 Artifact.TimelineKey（导出任务快照中的 timelineKey）。
	TimelineKey string
	// SourceRevisionNo / SourceDisplayName 见 Artifact.SourceRevisionNo / SourceDisplayName。
	SourceRevisionNo   int
	SourceDisplayName string
}

var ErrNotFound = errors.New("artifact: not found")

type Store interface {
	Create(ctx context.Context, tenantID string, in NewArtifact) (*Artifact, error)
	Get(ctx context.Context, tenantID, id string) (*Artifact, error)
	ListByProject(ctx context.Context, tenantID, projectID string) ([]*Artifact, error)
	// ListAll 返回租户内全部项目成品（按创建时间倒序），供跨项目成品库（B5-M2）。
	// ProjectName 经 LEFT JOIN projects 填充；owner 级读权限由 api 层 requireRole 控制。
	ListAll(ctx context.Context, tenantID string) ([]*Artifact, error)
	// Delete 删除一条成品记录（成品库「删除」）。仅删记录；成品对象由 api 层 BestEffort 清理。
	// 记录不存在时返回 ErrNotFound。
	Delete(ctx context.Context, tenantID, id string) error
}
