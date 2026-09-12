package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/pipeline"
)

// JobStore 是 JobService 需要的任务查询与操作能力（G3-9）。
type JobStore interface {
	JobCreator
	Get(ctx context.Context, id, tenantID string) (*pipeline.Job, error)
	List(ctx context.Context, tenantID, projectID, state, cursor string, pageSize int) ([]*pipeline.Job, string, error)
	Cancel(ctx context.Context, id, tenantID string) (*pipeline.Job, error)
	RetryFailed(ctx context.Context, id, tenantID string) (*pipeline.Job, error)
}

// JobService 提供任务查询、取消与重试（V4.0 §11.1）。
type JobService struct {
	pptsv1connect.UnimplementedJobServiceHandler
	jobs JobStore
}

// NewJobService 创建任务服务。
func NewJobService(jobs JobStore) *JobService { return &JobService{jobs: jobs} }

func (s *JobService) Get(ctx context.Context, req *connect.Request[pptsv1.GetJobRequest]) (*connect.Response[pptsv1.Job], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobID := strings.TrimSpace(req.Msg.GetJobId())
	if jobID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job_id is required"))
	}
	job, err := s.jobs.Get(ctx, jobID, p.TenantID)
	if err != nil {
		return nil, jobError(err)
	}
	return connect.NewResponse(toProtoJob(job)), nil
}

func (s *JobService) List(ctx context.Context, req *connect.Request[pptsv1.ListJobsRequest]) (*connect.Response[pptsv1.ListJobsResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	state := jobStateString(req.Msg.GetState())
	cursor := ""
	if req.Msg.GetCursor() != nil {
		cursor = req.Msg.GetCursor().GetValue()
	}
	jobs, next, err := s.jobs.List(ctx, p.TenantID, projectID, state, cursor, int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, jobError(err)
	}
	out := make([]*pptsv1.Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toProtoJob(job))
	}
	resp := &pptsv1.ListJobsResponse{Jobs: out}
	if next != "" {
		resp.NextCursor = &pptsv1.Cursor{Value: next}
	}
	return connect.NewResponse(resp), nil
}

func (s *JobService) Cancel(ctx context.Context, req *connect.Request[pptsv1.CancelJobRequest]) (*connect.Response[pptsv1.Job], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobID := strings.TrimSpace(req.Msg.GetJobId())
	if jobID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job_id is required"))
	}
	job, err := s.jobs.Cancel(ctx, jobID, p.TenantID)
	if err != nil {
		return nil, jobError(err)
	}
	return connect.NewResponse(toProtoJob(job)), nil
}

func (s *JobService) RetryFailed(ctx context.Context, req *connect.Request[pptsv1.RetryFailedRequest]) (*connect.Response[pptsv1.Job], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobID := strings.TrimSpace(req.Msg.GetJobId())
	if jobID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job_id is required"))
	}
	job, err := s.jobs.RetryFailed(ctx, jobID, p.TenantID)
	if err != nil {
		return nil, jobError(err)
	}
	return connect.NewResponse(toProtoJob(job)), nil
}

func toProtoJob(job *pipeline.Job) *pptsv1.Job {
	out := &pptsv1.Job{
		JobId: job.ID, ProjectId: job.ProjectID, Kind: string(job.Kind),
		State: jobStateProto(job.State), Attempt: int64(job.Attempt),
		ProgressPercent: int32(job.Progress), InputSnapshot: job.InputSnapshot,
		CreatedAtUnix: job.CreatedAt.Unix(), UpdatedAtUnix: job.UpdatedAt.Unix(),
	}
	if job.LastError != nil {
		out.LastError = &pptsv1.ErrorInfo{
			Code: job.LastError.Code, Message: job.LastError.Message,
			Retryable: job.LastError.Retryable, RetryAfterSeconds: int64(job.LastError.RetryAfterSeconds),
			TraceId: job.LastError.TraceID,
		}
	}
	return out
}

func jobStateProto(s pipeline.JobState) pptsv1.JobState {
	switch s {
	case pipeline.StateQueued:
		return pptsv1.JobState_JOB_STATE_QUEUED
	case pipeline.StateRunning:
		return pptsv1.JobState_JOB_STATE_RUNNING
	case pipeline.StateRetryWait:
		return pptsv1.JobState_JOB_STATE_RETRY_WAIT
	case pipeline.StateWaitingReview:
		return pptsv1.JobState_JOB_STATE_WAITING_REVIEW
	case pipeline.StateSucceeded:
		return pptsv1.JobState_JOB_STATE_SUCCEEDED
	case pipeline.StateFailed:
		return pptsv1.JobState_JOB_STATE_FAILED
	case pipeline.StateCancelReq:
		return pptsv1.JobState_JOB_STATE_CANCEL_REQUESTED
	case pipeline.StateCanceled:
		return pptsv1.JobState_JOB_STATE_CANCELED
	case pipeline.StateUnknownResult:
		return pptsv1.JobState_JOB_STATE_UNKNOWN_PROVIDER_RESULT
	default:
		return pptsv1.JobState_JOB_STATE_UNSPECIFIED
	}
}

func jobStateString(s pptsv1.JobState) string {
	switch s {
	case pptsv1.JobState_JOB_STATE_QUEUED:
		return string(pipeline.StateQueued)
	case pptsv1.JobState_JOB_STATE_RUNNING:
		return string(pipeline.StateRunning)
	case pptsv1.JobState_JOB_STATE_RETRY_WAIT:
		return string(pipeline.StateRetryWait)
	case pptsv1.JobState_JOB_STATE_WAITING_REVIEW:
		return string(pipeline.StateWaitingReview)
	case pptsv1.JobState_JOB_STATE_SUCCEEDED:
		return string(pipeline.StateSucceeded)
	case pptsv1.JobState_JOB_STATE_FAILED:
		return string(pipeline.StateFailed)
	case pptsv1.JobState_JOB_STATE_CANCEL_REQUESTED:
		return string(pipeline.StateCancelReq)
	case pptsv1.JobState_JOB_STATE_CANCELED:
		return string(pipeline.StateCanceled)
	case pptsv1.JobState_JOB_STATE_UNKNOWN_PROVIDER_RESULT:
		return string(pipeline.StateUnknownResult)
	default:
		return ""
	}
}

func jobError(err error) error {
	switch {
	case errors.Is(err, pipeline.ErrJobNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, pipeline.ErrJobNotCancelable), errors.Is(err, pipeline.ErrJobNotRetryable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
