package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/narration"
)

const defaultLanguage = "zh-CN"

// ScriptService exposes the narration revision domain over Connect.
type ScriptService struct {
	pptsv1connect.UnimplementedScriptServiceHandler
	store narration.Store
}

// NewScriptService creates a ScriptService.
func NewScriptService(store narration.Store) *ScriptService {
	return &ScriptService{store: store}
}

func (s *ScriptService) Get(ctx context.Context, req *connect.Request[pptsv1.GetScriptRequest]) (*connect.Response[pptsv1.ScriptRevision], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	rev, err := s.store.Get(ctx, p.TenantID, req.Msg.GetProjectId(), req.Msg.GetSlideId(), requestLanguage(req.Header()))
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(toProtoRevision(rev)), nil
}

func (s *ScriptService) Update(ctx context.Context, req *connect.Request[pptsv1.UpdateScriptRequest]) (*connect.Response[pptsv1.UpdateScriptResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	segments, err := fromProtoSegments(req.Msg.GetSegments())
	if err != nil {
		return nil, err
	}
	rev, err := s.store.Update(ctx, p.TenantID, req.Msg.GetProjectId(), req.Msg.GetSlideId(), requestLanguage(req.Header()), req.Msg.GetExpectedRevision(), segments)
	var conflict *narration.ErrConflict
	if errors.As(err, &conflict) {
		return connect.NewResponse(&pptsv1.UpdateScriptResponse{Conflict: true, Latest: toProtoRevision(conflict.Latest)}), nil
	}
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(&pptsv1.UpdateScriptResponse{Revision: toProtoRevision(rev)}), nil
}

func (s *ScriptService) Approve(ctx context.Context, req *connect.Request[pptsv1.ApproveScriptRequest]) (*connect.Response[pptsv1.ScriptRevision], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	rev, err := s.store.SetStatus(ctx, p.TenantID, req.Msg.GetProjectId(), req.Msg.GetSlideId(), requestLanguage(req.Header()), narration.StatusApproved)
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(toProtoRevision(rev)), nil
}

func (s *ScriptService) Lock(ctx context.Context, req *connect.Request[pptsv1.LockScriptRequest]) (*connect.Response[pptsv1.ScriptRevision], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	if !req.Msg.GetLock() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("locked scripts cannot be unlocked"))
	}
	rev, err := s.store.SetStatus(ctx, p.TenantID, req.Msg.GetProjectId(), req.Msg.GetSlideId(), requestLanguage(req.Header()), narration.StatusLocked)
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(toProtoRevision(rev)), nil
}

func requirePrincipal(ctx context.Context) (Principal, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication context is missing"))
	}
	return p, nil
}

func requireProjectSlide(projectID, slideID string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(slideID) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and slide_id are required"))
	}
	return nil
}

func requestLanguage(header httpHeader) string {
	language := strings.TrimSpace(strings.Split(header.Get("Accept-Language"), ",")[0])
	if language == "" {
		return defaultLanguage
	}
	if i := strings.IndexByte(language, ';'); i >= 0 {
		language = strings.TrimSpace(language[:i])
	}
	return language
}

type httpHeader interface {
	Get(string) string
}

func fromProtoSegments(in []*pptsv1.Segment) ([]*narration.Segment, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]*narration.Segment, 0, len(in))
	for _, segment := range in {
		if segment == nil || strings.TrimSpace(segment.GetSegmentId()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("every segment requires segment_id"))
		}
		if _, exists := seen[segment.GetSegmentId()]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate segment_id"))
		}
		seen[segment.GetSegmentId()] = struct{}{}
		out = append(out, &narration.Segment{
			SegmentID:   segment.GetSegmentId(),
			DisplayText: segment.GetDisplayText(),
			SpokenText:  segment.GetSpokenText(),
			SourceRefs:  append([]string(nil), segment.GetSourceRefs()...),
			Status:      narration.ScriptStatus(segment.GetStatus()),
		})
	}
	return out, nil
}

func toProtoRevision(rev *narration.Revision) *pptsv1.ScriptRevision {
	if rev == nil {
		return nil
	}
	segments := make([]*pptsv1.Segment, 0, len(rev.Segments))
	for _, segment := range rev.Segments {
		segments = append(segments, &pptsv1.Segment{
			SegmentId:   segment.SegmentID,
			SlideId:     rev.SlideID,
			DisplayText: segment.DisplayText,
			SpokenText:  segment.SpokenText,
			SourceRefs:  append([]string(nil), segment.SourceRefs...),
			Status:      string(segment.Status),
		})
	}
	return &pptsv1.ScriptRevision{
		ProjectId:     rev.ProjectID,
		SlideId:       rev.SlideID,
		Language:      rev.Language,
		Mode:          toProtoMode(rev.Mode),
		Revision:      rev.Revision,
		Status:        string(rev.Status),
		Segments:      segments,
		UpdatedAtUnix: rev.UpdatedAt.Unix(),
	}
}

func toProtoMode(mode narration.ScriptMode) pptsv1.ScriptMode {
	switch mode {
	case narration.ModeOriginal:
		return pptsv1.ScriptMode_SCRIPT_MODE_ORIGINAL
	case narration.ModePolish:
		return pptsv1.ScriptMode_SCRIPT_MODE_POLISH
	case narration.ModeAIGenerated:
		return pptsv1.ScriptMode_SCRIPT_MODE_AI_GENERATED
	default:
		return pptsv1.ScriptMode_SCRIPT_MODE_UNSPECIFIED
	}
}

func scriptError(err error) error {
	switch {
	case errors.Is(err, narration.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, narration.ErrLocked):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
