package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/F31/ppts/internal/tenant"
)

// HandlerFunc 执行单个任务。返回 error：*RetryError → 按时间退避重跑；
// 其他错误 → 置为 failed；nil → succeeded。
// handler 必须响应 workCtx 取消（取消请求/worker 停机）。
type HandlerFunc func(ctx context.Context, job *Job) error

// Claimer 是独立的任务领取能力（ADR-018 双连接部署：scheduler 连接只领取，
// handler 执行与终态提交走业务连接）。为零值即复用 Store 自身领取。
type Claimer interface {
	ClaimNext(ctx context.Context, tenantID, leaseOwner string, leaseFor time.Duration) (*Job, error)
	ClaimNextAny(ctx context.Context, leaseOwner string, leaseFor time.Duration) (*Job, error)
}

// Worker 是任务执行器（V4.0 §10.2）：
// 短事务领取 → 事务外执行 + 心跳续租 → fencing 条件提交；崩溃不写终态、租约到期重领取。
type Worker struct {
	store       Store
	claimer     Claimer
	owner       string
	tenantID    string
	leaseFor    time.Duration
	heartbeat   time.Duration
	poll        time.Duration
	handler     HandlerFunc
	backoff     func(attempt int) time.Duration
	maxAttempts int
	logger      *log.Logger
	metrics     WorkerMetrics
	onCanceled  func(context.Context, *Job) error
	// claimMu 串行化进程内的领取。SQLite 的 ClaimNext 是「SELECT 后 UPDATE」的延迟事务，
	// 多 goroutine 并发领取会相互 BUSY/重领；领取本身极短，加锁成本可忽略，处理仍并发。
	claimMu sync.Mutex
}

// WorkerOptions Worker 构造参数（零值给默认）。
type WorkerOptions struct {
	LeaseFor  time.Duration // 租约时长（默认 30s）
	Heartbeat time.Duration // 心跳间隔（默认 lease/3）
	Poll      time.Duration // 无任务轮询间隔（默认 500ms）
	Backoff   func(attempt int) time.Duration
	// MaxAttempts 单任务最大执行次数（含首次）。达到上限后不再自动重试，
	// 可重试错误也会落为失败终态，交给用户手动重试（Jobs 列表可 Retry）。
	// 默认 10。
	MaxAttempts int
	Logger      *log.Logger
	Metrics     WorkerMetrics
	OnCanceled  func(context.Context, *Job) error
	// Claimer 指定的独立领取器（跨租户调度连接）。nil 时使用 store 领取。
	Claimer Claimer
}

// WorkerMetrics 是 worker 的低基数观测 hook（按租户/任务类型/终态聚合）。
type WorkerMetrics interface {
	JobClaimed(job *Job)
	JobCompleted(job *Job, state JobState, duration time.Duration)
	JobRetryScheduled(job *Job)
	JobCancelRequested(job *Job)
	JobLeaseLost(job *Job)
}

type noopWorkerMetrics struct{}

func (noopWorkerMetrics) JobClaimed(*Job)                            {}
func (noopWorkerMetrics) JobCompleted(*Job, JobState, time.Duration) {}
func (noopWorkerMetrics) JobRetryScheduled(*Job)                     {}
func (noopWorkerMetrics) JobCancelRequested(*Job)                    {}
func (noopWorkerMetrics) JobLeaseLost(*Job)                          {}

// NewWorker 创建 worker。
func NewWorker(store Store, owner, tenantID string, handler HandlerFunc, opts WorkerOptions) *Worker {
	w := &Worker{store: store, claimer: opts.Claimer, owner: owner, tenantID: tenantID, handler: handler}
	w.leaseFor = opts.LeaseFor
	if w.leaseFor <= 0 {
		w.leaseFor = 30 * time.Second
	}
	w.heartbeat = opts.Heartbeat
	if w.heartbeat <= 0 {
		w.heartbeat = w.leaseFor / 3
	}
	w.poll = opts.Poll
	if w.poll <= 0 {
		w.poll = 500 * time.Millisecond
	}
	w.backoff = opts.Backoff
	if w.backoff == nil {
		w.backoff = func(attempt int) time.Duration { return time.Duration(1<<uint(min(attempt, 5))) * time.Second }
	}
	w.maxAttempts = opts.MaxAttempts
	if w.maxAttempts <= 0 {
		w.maxAttempts = 10
	}
	w.logger = opts.Logger
	if w.logger == nil {
		w.logger = log.Default()
	}
	w.metrics = opts.Metrics
	if w.metrics == nil {
		w.metrics = noopWorkerMetrics{}
	}
	w.onCanceled = opts.OnCanceled
	return w
}

// Run 进入领取循环，直到 ctx 取消或不可恢复错误。
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := w.claimNext(ctx)
		switch {
		case err == nil:
			w.metrics.JobClaimed(job)
			w.process(ctx, job)
		case err == ErrNoJob:
			if err := sleepCtx(ctx, w.poll); err != nil {
				return err
			}
		default:
			return err
		}
	}
}

func (w *Worker) claimNext(ctx context.Context) (*Job, error) {
	w.claimMu.Lock()
	defer w.claimMu.Unlock()
	if w.claimer != nil {
		if w.tenantID == "" {
			return w.claimer.ClaimNextAny(ctx, w.owner, w.leaseFor)
		}
		return w.claimer.ClaimNext(ctx, w.tenantID, w.owner, w.leaseFor)
	}
	if w.tenantID == "" {
		return w.store.ClaimNextAny(ctx, w.owner, w.leaseFor)
	}
	return w.store.ClaimNext(ctx, w.tenantID, w.owner, w.leaseFor)
}

// process 执行单任务：心跳驱动 handler，取消时不做终态提交（留给租约过期重领取）。
func (w *Worker) process(ctx context.Context, job *Job) {
	started := time.Now()
	// 注入任务所属租户，供续租/终态提交与 handler 建立 RLS 上下文。
	ctx = tenant.WithContext(ctx, job.TenantID)
	// 注入本轮租约凭据：handler 沿途所有步骤写入都据此校验归属，
	// 租约过期（已被他人重领）或任务已终态时写入被拒，避免旧 worker 污染新持有者的步骤。
	ctx = WithStepLease(ctx, job.LeaseOwner, job.FencingToken)

	// 领取到"待取消"任务：不执行 handler，直接提交 canceled。
	if job.State == StateCancelReq {
		if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateCanceled, nil); cerr != nil {
			w.logger.Printf("worker: complete pending-cancel job=%s: %v", job.ID, cerr)
		} else {
			w.releaseCanceled(ctx, job)
			w.metrics.JobCompleted(job, StateCanceled, time.Since(started))
		}
		return
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 注入进度上报：handler 可通过 pipeline.ReportProgress(ctx, pct) 上报 0-100。
	workCtx = context.WithValue(workCtx, progressKey{}, progressFunc(func(ctx context.Context, pct int) error {
		return w.store.UpdateProgress(ctx, job.ID, job.LeaseOwner, job.FencingToken, pct)
	}))
	// 注入 outbox：handler 可通过 pipeline.SetCommitStep(ctx, step) 声明随终态原子提交的最终步骤。
	commitStep := &commitStepHolder{}
	workCtx = context.WithValue(workCtx, commitStepKey{}, commitStep)

	canceled := &atomic.Bool{}
	hbDone := make(chan struct{})
	go w.heartbeatRun(workCtx, job, hbDone, cancel, canceled)

	err := w.handler(workCtx, job)
	cancel()
	<-hbDone

	// 父 ctx 取消 = worker 停机/崩溃：不写终态，靠租约过期让其他 worker 重领取。
	if ctx.Err() != nil {
		w.logger.Printf("worker: ctx canceled for job %s, leaving for reclaim", job.ID)
		w.metrics.JobLeaseLost(job)
		return
	}
	// 心跳发现取消请求：安全点停止并提交 canceled。
	if canceled.Load() {
		if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateCanceled, nil); cerr != nil {
			w.logger.Printf("worker: complete canceled job=%s: %v", job.ID, cerr)
		} else {
			w.releaseCanceled(ctx, job)
			w.metrics.JobCompleted(job, StateCanceled, time.Since(started))
		}
		return
	}
	if err != nil {
		if unknown := AsUnknownResult(err); unknown != nil {
			if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateUnknownResult, TryMarshalJobError(unknown)); cerr != nil {
				w.logger.Printf("worker: complete unknown-result job=%s: %v", job.ID, cerr)
			} else {
				w.metrics.JobCompleted(job, StateUnknownResult, time.Since(started))
			}
			return
		}
		if retry := AsRetry(err); retry != nil {
			if job.Attempt >= w.maxAttempts {
				// 达到最大执行次数：不再自动重试。TTS/供应商持续不可达（如 TLS 握手超时）时，
				// 无限轮换"等待重试/处理中"只会空耗 worker；落为失败终态，用户恢复后可在任务列表手动重试。
				if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateFailed, marshalMaxAttemptsError(retry, w.maxAttempts)); cerr != nil {
					w.logger.Printf("worker: complete failed after max attempts job=%s: %v", job.ID, cerr)
				} else {
					w.metrics.JobCompleted(job, StateFailed, time.Since(started))
				}
				w.logger.Printf("worker: job %s exhausted %d attempts, marked failed", job.ID, job.Attempt)
				return
			}
			at := retry.At
			if at.IsZero() {
				at = time.Now().Add(w.backoff(job.Attempt))
			}
			if jerr := w.store.ScheduleRetry(ctx, job.ID, job.LeaseOwner, job.FencingToken, at, marshalRetryError(retry, at)); jerr != nil {
				w.logger.Printf("worker: schedule retry failed job=%s: %v", job.ID, jerr)
			} else {
				w.metrics.JobRetryScheduled(job)
			}
			w.logger.Printf("worker: job %s scheduled retry at %s", job.ID, at)
			return
		}
		if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateFailed, TryMarshalJobError(err)); cerr != nil {
			w.logger.Printf("worker: complete failed job=%s: %v", job.ID, cerr)
		} else {
			w.metrics.JobCompleted(job, StateFailed, time.Since(started))
		}
		return
	}
	if err == nil && commitStep.step != nil {
		// 补上本轮租约凭据：让 outbox 步骤同样受 fencing/终态守卫约束，
		// 否则过期 worker 的最终步骤会绕过 MarkStep 的校验直接落库。
		commitStep.step.LeaseOwner = job.LeaseOwner
		commitStep.step.FencingToken = job.FencingToken
		if completer, ok := w.store.(CompleteWithStep); ok {
			if cerr := completer.CompleteWithStep(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateSucceeded, nil, commitStep.step); cerr != nil {
				w.logger.Printf("worker: complete succeeded (outbox) job=%s: %v", job.ID, cerr)
			} else {
				w.metrics.JobCompleted(job, StateSucceeded, time.Since(started))
			}
			return
		}
		// 后端未实现 outbox：回退为先写步骤再提交终态。
		if err := w.store.MarkStep(ctx, *commitStep.step); err != nil {
			w.logger.Printf("worker: mark outbox step failed job=%s: %v", job.ID, err)
		}
	}
	if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateSucceeded, nil); cerr != nil {
		w.logger.Printf("worker: complete succeeded job=%s: %v", job.ID, cerr)
	} else {
		w.metrics.JobCompleted(job, StateSucceeded, time.Since(started))
	}
}

func (w *Worker) releaseCanceled(ctx context.Context, job *Job) {
	if w.onCanceled == nil {
		return
	}
	if err := w.onCanceled(ctx, job); err != nil {
		w.logger.Printf("worker: on canceled job=%s: %v", job.ID, err)
	}
}

// heartbeatRun 周期性续租；fencing 失效或取消请求时终止本 worker 处理。
func (w *Worker) heartbeatRun(ctx context.Context, job *Job, done chan<- struct{}, cancel context.CancelFunc, canceled *atomic.Bool) {
	defer close(done)
	t := time.NewTicker(w.heartbeat)
	defer t.Stop()
	bg := tenant.WithContext(context.Background(), job.TenantID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.store.Heartbeat(bg, job.ID, job.LeaseOwner, job.FencingToken, w.leaseFor); err != nil {
				if errors.Is(err, ErrCancelRequested) {
					canceled.Store(true)
					w.metrics.JobCancelRequested(job)
					cancel()
				}
				// 租约丢失（如被重领取）：终止本 worker 处理，其提交自然失败。
				return
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// marshalRetryError 序列化可重试错误：Retryable=true 并带重试间隔，供 last_error 存储。
func marshalRetryError(r *RetryError, at time.Time) []byte {
	je := JobError{Code: "retryable", Message: r.Err.Error(), Retryable: true}
	if d := time.Until(at); d > 0 {
		je.RetryAfterSeconds = int(d / time.Second)
	}
	b, _ := json.Marshal(je)
	return b
}

// marshalMaxAttemptsError 序列化"重试次数耗尽"的失败：保留底层可重试信息，提示用户手动重试。
func marshalMaxAttemptsError(r *RetryError, attempts int) []byte {
	je := JobError{
		Code:      "retries_exhausted",
		Message:   fmt.Sprintf("job failed after %d attempts: %v", attempts, r.Err),
		Retryable: true,
	}
	b, _ := json.Marshal(je)
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
