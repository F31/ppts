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
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

const defaultLanguage = "zh-CN"

// ScriptService exposes the narration revision domain over Connect.
type ScriptService struct {
	pptsv1connect.UnimplementedScriptServiceHandler
	store   narration.Store
	jobs    JobCreator
	members membership.Reader
}

// NewScriptService creates a ScriptService.
func NewScriptService(store narration.Store, jobs JobCreator, members membership.Reader) *ScriptService {
	return &ScriptService{store: store, jobs: jobs, members: members}
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
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
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
	if err := requireRole(ctx, s.members, membership.RoleReviewer); err != nil {
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
	if err := requireRole(ctx, s.members, membership.RoleReviewer); err != nil {
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

func (s *ScriptService) GenerateDraft(ctx context.Context, req *connect.Request[pptsv1.GenerateDraftRequest]) (*connect.Response[pptsv1.GenerateDraftResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	idempotencyKey := strings.TrimSpace(req.Header().Get("Idempotency-Key"))
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Idempotency-Key header is required"))
	}
	mode := toDomainMode(req.Msg.GetMode())
	if mode != narration.ModeOriginal {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("polish/AI draft generation is G2; only original mode is available"))
	}
	snapshot := app.ScriptDraftSnapshot{
		ProjectID: projectID, Language: requestLanguage(req.Header()), Mode: string(mode),
	}
	for _, rawSlideID := range req.Msg.GetSlideIds() {
		slideID := strings.TrimSpace(rawSlideID)
		if slideID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_id cannot be empty"))
		}
		snapshot.SlideIDs = append(snapshot.SlideIDs, slideID)
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	job, err := s.jobs.Create(ctx, p.TenantID, projectID, string(pipeline.KindScriptDraft), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	return connect.NewResponse(&pptsv1.GenerateDraftResponse{JobId: job.ID, FullySupported: true}), nil
}

func toDomainMode(mode pptsv1.ScriptMode) narration.ScriptMode {
	switch mode {
	case pptsv1.ScriptMode_SCRIPT_MODE_ORIGINAL:
		return narration.ModeOriginal
	case pptsv1.ScriptMode_SCRIPT_MODE_POLISH:
		return narration.ModePolish
	case pptsv1.ScriptMode_SCRIPT_MODE_AI_GENERATED:
		return narration.ModeAIGenerated
	default:
		return narration.ModeOriginal
	}
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
