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
	ID           string
	TenantID     string
	ProjectID    string
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
}
