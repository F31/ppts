package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/tenant"
)

type PlaybackService struct {
	pptsv1connect.UnimplementedPlaybackServiceHandler
	jobs    JobCreator
	objects objectstore.ObjectStore
	parser  signedURLParser
}

func NewPlaybackService(jobs JobCreator, objects objectstore.ObjectStore) *PlaybackService {
	parser, _ := objects.(signedURLParser)
	return &PlaybackService{jobs: jobs, objects: objects, parser: parser}
}

func (s *PlaybackService) GetNarration(ctx context.Context, req *connect.Request[pptsv1.GetNarrationRequest]) (*connect.Response[pptsv1.GetNarrationResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	job, err := s.jobs.LatestSucceededJob(ctx, p.TenantID, projectID, string(pipeline.KindNarration))
	if errors.Is(err, pipeline.ErrNoSucceededJob) {
		return connect.NewResponse(&pptsv1.GetNarrationResponse{Ready: false}), nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	timelineKey, err := s.jobs.StepResultRef(tenant.WithContext(ctx, p.TenantID), job.ID, "timeline")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if timelineKey == "" {
		return connect.NewResponse(&pptsv1.GetNarrationResponse{Ready: false}), nil
	}
	resp := &pptsv1.GetNarrationResponse{Ready: true, TimelineKey: timelineKey}
	if pages, err := s.pagePngKeys(ctx, p.TenantID, projectID, timelineKey); err == nil {
		resp.PagePngKeys = pages
	}
	return connect.NewResponse(resp), nil
}

// pagePngKeys 按 timeline 页序返回页面 PNG 键；解析阶段未渲染或页数不齐时返回空（优雅降级为音频+字幕）。
func (s *PlaybackService) pagePngKeys(ctx context.Context, tenantID, projectID, timelineKey string) ([]string, error) {
	parseJob, err := s.jobs.LatestSucceededJob(ctx, tenantID, projectID, string(pipeline.KindParse))
	if errors.Is(err, pipeline.ErrNoSucceededJob) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ref, err := s.jobs.StepResultRef(tenant.WithContext(ctx, tenantID), parseJob.ID, "pages")
	if err != nil || ref == "" {
		return nil, err
	}
	manifest, err := s.loadPageManifest(ctx, tenantID, ref)
	if err != nil {
		return nil, err
	}
	bundle, _, err := s.loadBundle(ctx, tenantID, timelineKey)
	if err != nil {
		return nil, err
	}
	bySlide := make(map[string]string, len(manifest.Pages))
	for _, pg := range manifest.Pages {
		if pg.SlideID != "" {
			bySlide[pg.SlideID] = pg.Key
		}
	}
	out := make([]string, 0, len(bundle.Timeline.Slides))
	for _, slide := range bundle.Timeline.Slides {
		key, ok := bySlide[slide.SlideID]
		if !ok {
			return nil, nil
		}
		out = append(out, key)
	}
	return out, nil
}

func (s *PlaybackService) loadPageManifest(ctx context.Context, tenantID, rawKey string) (*app.PageManifest, error) {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return nil, err
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return nil, err
	}
	r, _, err := s.objects.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var manifest app.PageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (s *PlaybackService) GetManifest(ctx context.Context, req *connect.Request[pptsv1.GetPlaybackManifestRequest]) (*connect.Response[pptsv1.PlaybackManifest], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetProjectId() == "" || req.Msg.GetTimelineKey() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and timeline_key are required"))
	}
	ttl := time.Duration(req.Msg.GetTtlSeconds()) * time.Second
	if ttl <= 0 || ttl > 24*time.Hour {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ttl_seconds must be between 1 and 86400"))
	}
	bundle, _, err := s.loadBundle(ctx, p.TenantID, req.Msg.GetTimelineKey())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	pagePNGKeys := req.Msg.GetPagePngKeys()
	if len(pagePNGKeys) != 0 && len(pagePNGKeys) != len(bundle.Timeline.Slides) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_png_keys count must match timeline slides when provided"))
	}
	expires := time.Now().Add(ttl).Unix()
	resources := make([]*pptsv1.PlaybackResource, 0, 2+len(pagePNGKeys)+len(bundle.Timeline.Slides))
	appendSigned := func(rawKey string, typ pptsv1.PlaybackResourceType, slideID, segmentID string) error {
		key, meta, err := s.statTenantObject(ctx, p.TenantID, rawKey)
		if err != nil {
			return err
		}
		signed, err := s.objects.SignedURL(ctx, key, objectstore.OpRead, ttl)
		if err != nil {
			return err
		}
		resources = append(resources, &pptsv1.PlaybackResource{
			Type: typ, Key: key.String(), SignedUrl: rewriteLocalSignedURL(s.parser, signed),
			ContentType: meta.ContentType, SizeBytes: meta.Size, ContentHash: meta.ContentHash,
			SlideId: slideID, SegmentId: segmentID,
		})
		return nil
	}
	if err := appendSigned(req.Msg.GetTimelineKey(), pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_TIMELINE, "", ""); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := appendSigned(bundle.SRTKey, pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_SUBTITLE_SRT, "", ""); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := appendSigned(bundle.VTTKey, pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_SUBTITLE_VTT, "", ""); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for i, rawKey := range pagePNGKeys {
		if err := appendSigned(rawKey, pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_PAGE_PNG, bundle.Timeline.Slides[i].SlideID, ""); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	seenAudio := map[string]struct{}{}
	for _, slide := range bundle.Timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := seenAudio[segment.AudioKey]; ok {
				continue
			}
			seenAudio[segment.AudioKey] = struct{}{}
			if err := appendSigned(segment.AudioKey, pptsv1.PlaybackResourceType_PLAYBACK_RESOURCE_TYPE_AUDIO, slide.SlideID, segment.SegmentID); err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
		}
	}
	timelineJSON, err := json.Marshal(bundle.Timeline)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pptsv1.PlaybackManifest{
		ProjectId: req.Msg.GetProjectId(), TimelineKey: req.Msg.GetTimelineKey(),
		TimelineJson: string(timelineJSON), Resources: resources, ExpiresAtUnix: expires,
	}), nil
}

func (s *PlaybackService) loadBundle(ctx context.Context, tenantID, rawKey string) (*app.TimelineAsset, objectstore.ObjectMeta, error) {
	key, meta, err := s.statTenantObject(ctx, tenantID, rawKey)
	if err != nil {
		return nil, objectstore.ObjectMeta{}, err
	}
	r, _, err := s.objects.Get(ctx, key)
	if err != nil {
		return nil, objectstore.ObjectMeta{}, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, objectstore.ObjectMeta{}, err
	}
	var bundle app.TimelineAsset
	if err := json.Unmarshal(data, &bundle); err != nil || bundle.Timeline == nil || bundle.SRTKey == "" || bundle.VTTKey == "" {
		return nil, objectstore.ObjectMeta{}, errors.New("invalid timeline bundle")
	}
	return &bundle, meta, nil
}

func (s *PlaybackService) statTenantObject(ctx context.Context, tenantID, rawKey string) (objectstore.ObjectKey, objectstore.ObjectMeta, error) {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return objectstore.ObjectKey{}, objectstore.ObjectMeta{}, err
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return objectstore.ObjectKey{}, objectstore.ObjectMeta{}, err
	}
	r, meta, err := s.objects.Get(ctx, key)
	if err != nil {
		return objectstore.ObjectKey{}, objectstore.ObjectMeta{}, err
	}
	if err := r.Close(); err != nil {
		return objectstore.ObjectKey{}, objectstore.ObjectMeta{}, err
	}
	return key, meta, nil
}
