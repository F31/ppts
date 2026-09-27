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
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
)

type ExportService struct {
	pptsv1connect.UnimplementedExportServiceHandler
	jobs      JobCreator
	artifacts artifact.Store
	objects   objectstore.ObjectStore
	parser    signedURLParser
	members   membership.Reader
	recorder  audit.Recorder
}

func NewExportService(jobs JobCreator, artifacts artifact.Store, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder) *ExportService {
	parser, _ := objects.(signedURLParser)
	return &ExportService{jobs: jobs, artifacts: artifacts, objects: objects, parser: parser, members: members, recorder: recorder}
}

// 导出/下载审计（P2-B2）。
//
// 导出与下载是"数据离开系统"的动作，此前完全无审计：谁在什么时候把哪个项目的成品
// 导出成什么格式、谁下载了哪个成品，事后一概查不到。对含未公开内容的项目来说，
// 这是最该留痕的一类操作。
//
// 脱敏约束：**绝不记录签名 URL**。签名 URL 是短期读凭据，写进审计等于把凭据复制进日志，
// 且审计保留期通常远长于 TTL —— 日志会变成一条永久有效的下载通道。
// 只记 TTL 这类非敏感参数，足以回答"下载窗口开得多大"。

// recordExport 记录导出/下载审计（recorder 为 nil 时跳过；失败不阻断业务，与既有审计调用一致）。
func recordExport(ctx context.Context, recorder audit.Recorder, tenantID, actor, action, resourceID string, meta map[string]any) {
	if recorder == nil || tenantID == "" {
		return
	}
	_ = recorder.Record(ctx, audit.Event{
		TenantID:     tenantID,
		ActorUser:    actor,
		Action:       action,
		ResourceType: "export",
		ResourceID:   resourceID,
		Metadata:     meta,
	})
}

func (s *ExportService) CreateExport(ctx context.Context, req *connect.Request[pptsv1.CreateExportRequest]) (*connect.Response[pptsv1.CreateExportResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
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
	// 时间轴必须属于本次导出的项目：跨项目时间轴会产出"内容来自 A、却挂在 B 名下"的成品，
	// 成品库预览时页图/字幕与项目对不上（历史事故）。此处显式拒绝。
	if err := ensureProjectKey(p.TenantID, projectID, req.Msg.GetTimelineKey()); err != nil {
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
	snapshot := app.ExportSnapshot{
		Format: format, TimelineKey: req.Msg.GetTimelineKey(), PagePNGKeys: pageKeys,
		FPS: 30, Width: 1920, Height: 1080,
		BurnSubtitles: req.Msg.GetBurnSubtitles(), IncludeNotes: req.Msg.GetIncludeNotes(),
	}
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
	recordExport(ctx, s.recorder, p.TenantID, p.UserID, "export.create", job.ID,
		map[string]any{"project_id": projectID, "format": string(format)})
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
	if err := requireRole(ctx, s.members, membership.RoleViewer); err != nil {
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
	// 只记 TTL 与成品归属，签名 URL 绝不落审计（见文件头脱敏约束）。
	recordExport(ctx, s.recorder, p.TenantID, p.UserID, "export.download", a.ID,
		map[string]any{"project_id": a.ProjectID, "format": string(a.Format), "ttl_seconds": int(ttl / time.Second)})
	return connect.NewResponse(&pptsv1.CreateDownloadResponse{SignedUrl: rewriteLocalSignedURL(s.parser, url), ExpiresAtUnix: time.Now().Add(ttl).Unix()}), nil
}

func exportFormat(format pptsv1.ArtifactFormat) (artifact.Format, error) {
	switch format {
	case pptsv1.ArtifactFormat_ARTIFACT_FORMAT_MP4:
		return artifact.FormatMP4, nil
	case pptsv1.ArtifactFormat_ARTIFACT_FORMAT_WEB_PROJECT:
		return artifact.FormatWebProject, nil
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

// ensureProjectKey 校验对象键属于指定项目，避免跨项目引用（成品绑定了别的项目的时间轴）。
func ensureProjectKey(tenantID, projectID, rawKey string) error {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	if key.ProjectID != projectID {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("timeline_key does not belong to the project"))
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
	case artifact.FormatWebProject:
		return pptsv1.ArtifactFormat_ARTIFACT_FORMAT_WEB_PROJECT
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
