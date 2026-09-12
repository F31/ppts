package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
)

// JobCreator 是 API transport 需要的任务能力（创建任务 + 播放服务发现最近成功配音）。
type JobCreator interface {
	Create(context.Context, string, string, string, string, string, time.Time) (*pipeline.Job, error)
	LatestSucceededJob(ctx context.Context, tenantID, projectID, kind string) (*pipeline.Job, error)
	StepResultRef(ctx context.Context, jobID, stepType string) (string, error)
}

// QuotaManager 是配音生成前的额度预占能力（G3-2）。
type QuotaManager interface {
	Reserve(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind, units float64) (*usage.Reservation, error)
	Release(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind) error
}

// NarrationGenerationService creates revision-bound narration jobs.
type NarrationGenerationService struct {
	pptsv1connect.UnimplementedNarrationServiceHandler
	scripts narration.Store
	jobs    JobCreator
	quota   QuotaManager
	policy  TenantPolicyReader
	members membership.Reader
}

// NewNarrationGenerationService creates a narration task service.
func NewNarrationGenerationService(scripts narration.Store, jobs JobCreator, quota QuotaManager, policy TenantPolicyReader, members membership.Reader) *NarrationGenerationService {
	return &NarrationGenerationService{scripts: scripts, jobs: jobs, quota: quota, policy: policy, members: members}
}

type activeJobCounter interface {
	CountActive(ctx context.Context, tenantID string) (int, error)
}

type jobByIdempotencyFinder interface {
	ByIdempotency(ctx context.Context, tenantID, kind, idemKey string) (*pipeline.Job, error)
}

func (s *NarrationGenerationService) CreateGeneration(ctx context.Context, req *connect.Request[pptsv1.CreateGenerationRequest]) (*connect.Response[pptsv1.CreateGenerationResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
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
	totalRunes := 0
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
		for _, segment := range revision.Segments {
			if segment != nil {
				totalRunes += utf8.RuneCountInString(segment.SpokenText)
			}
		}
		snapshot.Slides = append(snapshot.Slides, app.NarrationSlideSnapshot{
			SlideID: slideID, ScriptRevision: revision.Revision,
		})
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if existing, err := s.enforceConcurrentLimit(ctx, principal.TenantID, idempotencyKey, string(snapshotBytes)); err != nil {
		return nil, err
	} else if existing != nil {
		return connect.NewResponse(&pptsv1.CreateGenerationResponse{JobId: existing.ID, WithinBudget: true}), nil
	}

	// 配额预占：预占成功才创建可执行任务（V4.0 §12.2）。
	var reserved *usage.Reservation
	if s.quota != nil {
		units := usage.EstimateSeconds(totalRunes)
		res, rerr := s.quota.Reserve(ctx, principal.TenantID, idempotencyKey, usage.KindGenSeconds, units)
		if errors.Is(rerr, usage.ErrInsufficientQuota) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("quota exceeded for requested narration"))
		}
		if rerr != nil {
			return nil, connect.NewError(connect.CodeInternal, rerr)
		}
		reserved = res
	}
	releaseReservation := func() {
		if reserved != nil && reserved.Created {
			_ = s.quota.Release(ctx, principal.TenantID, idempotencyKey, usage.KindGenSeconds)
		}
	}
	job, err := s.jobs.Create(ctx, principal.TenantID, projectID, string(pipeline.KindNarration), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		releaseReservation()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		releaseReservation()
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	return connect.NewResponse(&pptsv1.CreateGenerationResponse{JobId: job.ID, WithinBudget: true}), nil
}

func (s *NarrationGenerationService) enforceConcurrentLimit(ctx context.Context, tenantID, idempotencyKey, snapshot string) (*pipeline.Job, error) {
	if s.policy == nil {
		return nil, nil
	}
	policy, err := s.policy.GetPolicy(ctx, tenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	if policy.MaxConcurrentJobs <= 0 {
		return nil, nil
	}
	counter, ok := s.jobs.(activeJobCounter)
	if !ok {
		return nil, nil
	}
	active, err := counter.CountActive(ctx, tenantID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if active < policy.MaxConcurrentJobs {
		return nil, nil
	}
	if finder, ok := s.jobs.(jobByIdempotencyFinder); ok {
		job, err := finder.ByIdempotency(ctx, tenantID, string(pipeline.KindNarration), idempotencyKey)
		if err == nil {
			if job.InputSnapshot != snapshot {
				return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
			}
			return job, nil
		}
		if !errors.Is(err, pipeline.ErrJobNotFound) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("tenant concurrent job limit reached"))
}

// Estimate 估算所选页面的播报秒数。定价随正式 TTS 供应商确定（当前返回 0 费用）。
func (s *NarrationGenerationService) Estimate(ctx context.Context, req *connect.Request[pptsv1.NarrationEstimateRequest]) (*connect.Response[pptsv1.NarrationEstimateResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" || len(req.Msg.GetSlideIds()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and slide_ids are required"))
	}
	language := requestLanguage(req.Header())
	totalRunes := 0
	for _, rawSlideID := range req.Msg.GetSlideIds() {
		slideID := strings.TrimSpace(rawSlideID)
		if slideID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_id cannot be empty"))
		}
		revision, err := s.scripts.Get(ctx, principal.TenantID, projectID, slideID, language)
		if err != nil {
			return nil, scriptError(err)
		}
		for _, segment := range revision.Segments {
			if segment != nil {
				totalRunes += utf8.RuneCountInString(segment.SpokenText)
			}
		}
	}
	return connect.NewResponse(&pptsv1.NarrationEstimateResponse{
		EstimatedSeconds: int64(math.Ceil(usage.EstimateSeconds(totalRunes))),
	}), nil
}
