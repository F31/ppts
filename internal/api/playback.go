package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

type PlaybackService struct {
	pptsv1connect.UnimplementedPlaybackServiceHandler
	objects objectstore.ObjectStore
}

func NewPlaybackService(objects objectstore.ObjectStore) *PlaybackService {
	return &PlaybackService{objects: objects}
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
	if len(req.Msg.GetPagePngKeys()) != len(bundle.Timeline.Slides) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_png_keys count must match timeline slides"))
	}
	expires := time.Now().Add(ttl).Unix()
	resources := make([]*pptsv1.PlaybackResource, 0, 2+len(req.Msg.GetPagePngKeys())+len(bundle.Timeline.Slides))
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
			Type: typ, Key: key.String(), SignedUrl: signed,
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
	for i, rawKey := range req.Msg.GetPagePngKeys() {
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
