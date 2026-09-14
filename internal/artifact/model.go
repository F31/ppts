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
	CreatedAt    time.Time
}

type NewArtifact struct {
	ProjectID    string
	SnapshotHash string
	Format       Format
	ObjectKey    string
	ContentHash  string
	SizeBytes    int64
}

var ErrNotFound = errors.New("artifact: not found")

type Store interface {
	Create(ctx context.Context, tenantID string, in NewArtifact) (*Artifact, error)
	Get(ctx context.Context, tenantID, id string) (*Artifact, error)
}
