// Package pipeline 负责任务状态、租约、重试、幂等与取消（V4.0 §10/§4.2）。
//
// 数据库是任务事实来源，channel 只做进程内并发控制。本包是领域层：定义
// Job/JobStep 与 Store 端口，不依赖 HTTP/云 SDK。PostgreSQL 实现见本包
// postgres.go（SKIP LOCKED 领取 + 租约 + fencing 条件提交）。
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// JobState 任务状态（与 0001_init.sql CHECK 保持一致）。
type JobState string

const (
	StateQueued        JobState = "queued"
	StateRunning       JobState = "running"
	StateRetryWait     JobState = "retry_wait"
	StateWaitingReview JobState = "waiting_review"
	StateSucceeded     JobState = "succeeded"
	StateFailed        JobState = "failed"
	StateCancelReq     JobState = "cancel_requested"
	StateCanceled      JobState = "canceled"
	StateUnknownResult JobState = "unknown_provider_result"
)

// JobKind 任务种类。
type JobKind string

const (
	KindParse       JobKind = "parse"
	KindRender      JobKind = "render"
	KindScriptDraft JobKind = "script_draft"
	KindNarration   JobKind = "narration"
	KindExport      JobKind = "export"
)

// Job 是任务真相（源自数据库行）。
type Job struct {
	ID             string
	TenantID       string
	ProjectID      string
	Kind           JobKind
	State          JobState
	InputSnapshot  string
	IDempotencyKey string
	Attempt        int
	LeaseOwner     string
	LeaseUntil     time.Time
	FencingToken   int64
	RunAt          time.Time
	Progress       int
	LastError      *JobError
	CreatedAt      time.Time
	UpdatedAt      time.Time
	TraceParent    string
}

// JobEvent 是任务变更事件（G3-9 WatchEvents 单调序号）。
type JobEvent struct {
	Seq int64
	Job *Job
}

// progressKey 把 worker 注入的进度上报函数放入任务执行上下文。
type progressKey struct{}

type progressFunc func(ctx context.Context, pct int) error

// ReportProgress 由 handler 调用以上报任务进度（0-100）。
// worker 未注入时静默忽略（例如独立执行 handler 的测试）。
func ReportProgress(ctx context.Context, pct int) error {
	if fn, ok := ctx.Value(progressKey{}).(progressFunc); ok {
		return fn(ctx, pct)
	}
	return nil
}

// commitStepKey 把 handler 声明的"随终态原子提交的最终步骤"放入任务执行上下文（outbox，G3-5）。
type commitStepKey struct{}

type commitStepHolder struct {
	step *JobStep
}

// SetCommitStep 由 handler 在成功收尾时调用：该步骤不会单独提交，而是随任务终态在同一事务写入。
// 适用于"完成后才存在的最终产物步骤"；中途步骤仍用 MarkStep 独立提交。
// 未在 worker 内（独立执行 handler 的测试）时静默忽略，调用方不应依赖立即持久化。
func SetCommitStep(ctx context.Context, step JobStep) {
	if h, ok := ctx.Value(commitStepKey{}).(*commitStepHolder); ok {
		h.step = &step
	}
}

// CompleteWithStep 是 Store 的可选能力：终态提交与最终步骤原子写入。
type CompleteWithStep interface {
	// CompleteWithStep 以 fencing 条件置为终态，并在同一事务 upsert 最终成功步骤。
	CompleteWithStep(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte, step *JobStep) error
}

// JobError 是结构化错误（V4.0 §11.1 code/message/retryable/retry_after）。
type JobError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
	TraceID           string `json:"traceId"`
}

// Terminal 报告该状态是否为终态（不可再领取）。
func (s JobState) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}

// Clerical 报告该状态是否可被领取器领取（queued 或到期 retry_wait）。
func (s JobState) Clerical() bool {
	return s == StateQueued || s == StateRetryWait
}

// JobStepState 步骤状态。
type JobStepState string

const (
	StepPending JobStepState = "pending"
	StepSuccess JobStepState = "success"
	StepSkipped JobStepState = "skipped"
	StepFailed  JobStepState = "failed"
)

// JobStep 是任务内的一个执行步骤（重试只重跑未确认完成的步骤，V4.0 §10.2）。
type JobStep struct {
	ID        string
	JobID     string
	TenantID  string
	StepType  string
	StepKey   string
	State     JobStepState
	ResultRef string
	UpdatedAt time.Time
}

// ErrNoJob 表示符合条件的可领取任务不存在。
var ErrNoJob = errors.New("pipeline: no claimable job")

// ErrLeaseMismatch 表示 fencing 校验失败（过期 worker 的提交无效）。
var ErrLeaseMismatch = errors.New("pipeline: job lease/fencing mismatch")

// ErrNoSucceededJob 表示某项目尚无成功完成的指定类型任务。
var ErrNoSucceededJob = errors.New("pipeline: no succeeded job")

// ErrCancelRequested 表示续租时发现任务已被请求取消，worker 应在安全点停止。
var ErrCancelRequested = errors.New("pipeline: cancel requested")

// ErrJobNotFound 表示任务不存在或越权。
var ErrJobNotFound = errors.New("pipeline: job not found")

// ErrJobNotCancelable 表示任务当前状态不可取消。
var ErrJobNotCancelable = errors.New("pipeline: job not cancelable")

// ErrJobNotRetryable 表示任务当前状态不可重试。
var ErrJobNotRetryable = errors.New("pipeline: job not retryable")

// RetryError 由 handler 返回以请求按 RetryAfter 退避重试（V4.0 §10.4）。
// 非 RetryError 的错误视为永久失败（不盲目重试）。
type RetryError struct {
	Err error
	At  time.Time // 建议下次运行时间；零值 = 默认退避
}

func (r *RetryError) Error() string { return r.Err.Error() }

func (r *RetryError) Unwrap() error { return r.Err }

// AsRetry 提取 *RetryError；非可重试错误返回 nil。
func AsRetry(err error) *RetryError {
	var r *RetryError
	if errors.As(err, &r) {
		return r
	}
	return nil
}

// UnknownResultError 表示供应商调用已发出但结果状态不可确认（超时/连接中断等）。
// worker 会把任务置为 unknown_provider_result，等待对账或人工重试。
type UnknownResultError struct {
	Err error
}

func (e *UnknownResultError) Error() string { return e.Err.Error() }

func (e *UnknownResultError) Unwrap() error { return e.Err }

func AsUnknownResult(err error) *UnknownResultError {
	var u *UnknownResultError
	if errors.As(err, &u) {
		return u
	}
	return nil
}

// TryMarshalJobError 将 error 转为 JobError JSON（供 last_error 存储）。
func TryMarshalJobError(err error) []byte {
	je := JobError{Code: "internal", Message: err.Error()}
	b, _ := json.Marshal(je)
	return b
}

// Store 任务存储端口。任何方法都要求显式 tenant_id（源自登录身份，非客户端传入）。
type Store interface {
	// Create 创建任务；同 (tenant, idempotency_key, kind) 幂等：命中唯一约束时
	// 返回已存在行而非新建。
	Create(ctx context.Context, tenantID, projectID, kind, idemKey, snapshot string, runAt time.Time) (*Job, error)
	// ClaimNext 以 SKIP LOCKED 领取一个可运行任务，原子写入 lease 与 fencing，返回租约内任务。
	ClaimNext(ctx context.Context, tenantID, leaseOwner string, leaseFor time.Duration) (*Job, error)
	// ClaimNextAny 跨租户领取一个可运行任务；仅调度面使用，返回任务 tenant_id 供 worker 执行前注入上下文。
	ClaimNextAny(ctx context.Context, leaseOwner string, leaseFor time.Duration) (*Job, error)
	// Heartbeat 续租；fencing 不匹配返回 ErrLeaseMismatch。
	Heartbeat(ctx context.Context, id, owner string, fencing int64, extend time.Duration) error
	// Complete 以 fencing 条件把任务置为终态（succeeded/failed/canceled）或 unknown_provider_result。
	Complete(ctx context.Context, id, owner string, fencing int64, state JobState, errMsg []byte) error
	// ScheduleRetry 把任务置为 retry_wait，带退避 run_at 与错误。
	ScheduleRetry(ctx context.Context, id, owner string, fencing int64, runAt time.Time, errMsg []byte) error
	// MarkStep 记录步骤结果；成功引用与步骤完成同事务提交（由调用方事务控制）。
	MarkStep(ctx context.Context, step JobStep) error
	// UpdateProgress 以 fencing 条件更新进度（state='running'）并记录事件。
	UpdateProgress(ctx context.Context, id, owner string, fencing int64, progress int) error
	// EventsSince 返回某项目在 afterSeq 之后的事件（升序），供 WatchEvents 断点续传。
	EventsSince(ctx context.Context, tenantID, projectID string, afterSeq int64, limit int) ([]JobEvent, error)
	// Cancel 取消任务：queued/retry_wait 直接置 canceled，running 置 cancel_requested
	// 由 worker 在安全点停止并提交 canceled。不可取消状态返回 ErrJobNotCancelable。
	Cancel(ctx context.Context, id, tenantID string) (*Job, error)
	// RetryFailed 将 failed/unknown_provider_result 任务重新入队（同任务行，保留 fencing 递增语义）。
	RetryFailed(ctx context.Context, id, tenantID string) (*Job, error)
	// List 按项目/状态游标分页查询（created_at 倒序）。
	List(ctx context.Context, tenantID, projectID, state, cursor string, pageSize int) ([]*Job, string, error)
	// Get 按 ID 查询（含跨步校验）。
	Get(ctx context.Context, id, tenantID string) (*Job, error)
}
