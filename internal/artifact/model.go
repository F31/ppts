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
	CreatedAt  time.Time
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
}

var ErrNotFound = errors.New("artifact: not found")

type Store interface {
	Create(ctx context.Context, tenantID string, in NewArtifact) (*Artifact, error)
	Get(ctx context.Context, tenantID, id string) (*Artifact, error)
	ListByProject(ctx context.Context, tenantID, projectID string) ([]*Artifact, error)
	// ListAll 返回租户内全部项目成品（按创建时间倒序），供跨项目成品库（B5-M2）。
	// ProjectName 经 LEFT JOIN projects 填充；owner 级读权限由 api 层 requireRole 控制。
	ListAll(ctx context.Context, tenantID string) ([]*Artifact, error)
}
