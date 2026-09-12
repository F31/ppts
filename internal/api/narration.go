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
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

// JobCreator 是 API transport 需要的任务能力（创建任务 + 播放服务发现最近成功配音）。
type JobCreator interface {
	Create(context.Context, string, string, string, string, string, time.Time) (*pipeline.Job, error)
	LatestSucceededJob(ctx context.Context, tenantID, projectID, kind string) (*pipeline.Job, error)
	StepResultRef(ctx context.Context, jobID, stepType string) (string, error)
}

// NarrationGenerationService creates revision-bound narration jobs.
type NarrationGenerationService struct {
	pptsv1connect.UnimplementedNarrationServiceHandler
	scripts narration.Store
	jobs    JobCreator
}

// NewNarrationGenerationService creates a narration task service.
func NewNarrationGenerationService(scripts narration.Store, jobs JobCreator) *NarrationGenerationService {
	return &NarrationGenerationService{scripts: scripts, jobs: jobs}
}

func (s *NarrationGenerationService) CreateGeneration(ctx context.Context, req *connect.Request[pptsv1.CreateGenerationRequest]) (*connect.Response[pptsv1.CreateGenerationResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	voiceID := strings.TrimSpace(req.Msg.GetVoiceId())
	if projectID == "" || voiceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and voice_id are required"))
	}
	if len(req.Msg.GetSlideIds()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_ids are required until project page-order lookup is available"))
	}
	rate := int(req.Msg.GetRatePercent())
	if rate == 0 {
		rate = 100
	}
	if rate < 50 || rate > 200 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate_percent must be between 50 and 200"))
	}
	idempotencyKey := strings.TrimSpace(req.Header().Get("Idempotency-Key"))
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Idempotency-Key header is required"))
	}

	language := requestLanguage(req.Header())
	snapshot := app.NarrationSnapshot{
		Language: language, VoiceID: voiceID, RequireConfirmed: req.Msg.GetLockConfirmedOnly(),
		SpeechControl: tts.SpeechControl{RatePercent: rate}, SampleRate: 16000,
	}
	seen := make(map[string]struct{}, len(req.Msg.GetSlideIds()))
	for _, rawSlideID := range req.Msg.GetSlideIds() {
		slideID := strings.TrimSpace(rawSlideID)
		if slideID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_id cannot be empty"))
		}
		if _, exists := seen[slideID]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate slide_id"))
		}
		seen[slideID] = struct{}{}
		revision, err := s.scripts.Get(ctx, principal.TenantID, projectID, slideID, language)
		if err != nil {
			return nil, scriptError(err)
		}
		if snapshot.RequireConfirmed && revision.Status != narration.StatusApproved && revision.Status != narration.StatusLocked {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("all selected scripts must be approved or locked"))
		}
		snapshot.Slides = append(snapshot.Slides, app.NarrationSlideSnapshot{
			SlideID: slideID, ScriptRevision: revision.Revision,
		})
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	job, err := s.jobs.Create(ctx, principal.TenantID, projectID, string(pipeline.KindNarration), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	return connect.NewResponse(&pptsv1.CreateGenerationResponse{JobId: job.ID, WithinBudget: true}), nil
}
