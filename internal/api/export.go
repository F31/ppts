package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
)

type ExportService struct {
	pptsv1connect.UnimplementedExportServiceHandler
	jobs      JobCreator
	artifacts artifact.Store
	objects   objectstore.ObjectStore
}

func NewExportService(jobs JobCreator, artifacts artifact.Store, objects objectstore.ObjectStore) *ExportService {
	return &ExportService{jobs: jobs, artifacts: artifacts, objects: objects}
}

func (s *ExportService) CreateExport(ctx context.Context, req *connect.Request[pptsv1.CreateExportRequest]) (*connect.Response[pptsv1.CreateExportResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	format, err := exportFormat(req.Msg.GetFormat())
	if err != nil {
		return nil, err
	}
	if projectID == "" || strings.TrimSpace(req.Msg.GetTimelineKey()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and timeline_key are required"))
	}
	if err := ensureTenantKey(p.TenantID, req.Msg.GetTimelineKey()); err != nil {
		return nil, err
	}
	pageKeys := append([]string(nil), req.Msg.GetPagePngKeys()...)
	if format == artifact.FormatMP4 && len(pageKeys) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_png_keys are required for mp4 export"))
	}
	for _, key := range pageKeys {
		if err := ensureTenantKey(p.TenantID, key); err != nil {
			return nil, err
		}
	}
	idempotencyKey := strings.TrimSpace(req.Header().Get("Idempotency-Key"))
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Idempotency-Key header is required"))
	}
	snapshot := app.ExportSnapshot{Format: format, TimelineKey: req.Msg.GetTimelineKey(), PagePNGKeys: pageKeys, FPS: 30, Width: 1920, Height: 1080}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	job, err := s.jobs.Create(ctx, p.TenantID, projectID, string(pipeline.KindExport), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	return connect.NewResponse(&pptsv1.CreateExportResponse{JobId: job.ID}), nil
}

func (s *ExportService) GetArtifact(ctx context.Context, req *connect.Request[pptsv1.GetArtifactRequest]) (*connect.Response[pptsv1.Artifact], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.artifacts.Get(ctx, p.TenantID, req.Msg.GetArtifactId())
	if err != nil {
		return nil, artifactError(err)
	}
	return connect.NewResponse(toProtoArtifact(a)), nil
}

func (s *ExportService) CreateDownload(ctx context.Context, req *connect.Request[pptsv1.CreateDownloadRequest]) (*connect.Response[pptsv1.CreateDownloadResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(req.Msg.GetTtlSeconds()) * time.Second
	if ttl <= 0 || ttl > 24*time.Hour {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ttl_seconds must be between 1 and 86400"))
	}
	a, err := s.artifacts.Get(ctx, p.TenantID, req.Msg.GetArtifactId())
	if err != nil {
		return nil, artifactError(err)
	}
	key, err := objectstore.Parse(a.ObjectKey)
	if err != nil || key.EnsureTenant(p.TenantID) != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("artifact object key is invalid"))
	}
	url, err := s.objects.SignedURL(ctx, key, objectstore.OpRead, ttl)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pptsv1.CreateDownloadResponse{SignedUrl: url, ExpiresAtUnix: time.Now().Add(ttl).Unix()}), nil
}

func exportFormat(format pptsv1.ArtifactFormat) (artifact.Format, error) {
	switch format {
	case pptsv1.ArtifactFormat_ARTIFACT_FORMAT_MP4:
		return artifact.FormatMP4, nil
	case pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT:
		return artifact.FormatSRT, nil
	case pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_VTT:
		return artifact.FormatVTT, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported export format"))
	}
}

func ensureTenantKey(tenantID, rawKey string) error {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return nil
}

func toProtoArtifact(a *artifact.Artifact) *pptsv1.Artifact {
	if a == nil {
		return nil
	}
	return &pptsv1.Artifact{
		ArtifactId: a.ID, ProjectId: a.ProjectID, Format: toProtoExportFormat(a.Format),
		ObjectKey: a.ObjectKey, ContentHash: a.ContentHash, SizeBytes: a.SizeBytes,
		CreatedAtUnix: a.CreatedAt.Unix(), SnapshotHash: a.SnapshotHash,
	}
}

func toProtoExportFormat(format artifact.Format) pptsv1.ArtifactFormat {
	switch format {
	case artifact.FormatMP4:
		return pptsv1.ArtifactFormat_ARTIFACT_FORMAT_MP4
	case artifact.FormatSRT:
		return pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_SRT
	case artifact.FormatVTT:
		return pptsv1.ArtifactFormat_ARTIFACT_FORMAT_SUBTITLE_VTT
	default:
		return pptsv1.ArtifactFormat_ARTIFACT_FORMAT_UNSPECIFIED
	}
}

func artifactError(err error) error {
	if errors.Is(err, artifact.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
