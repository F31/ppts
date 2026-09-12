package pipeline

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/F31/ppts/internal/tenant"
)

// HandlerFunc 执行单个任务。返回 error：*RetryError → 按时间退避重跑；
// 其他错误 → 置为 failed；nil → succeeded。
// handler 必须响应 workCtx 取消（取消请求/worker 停机）。
type HandlerFunc func(ctx context.Context, job *Job) error

// Worker 是任务执行器（V4.0 §10.2）：
// 短事务领取 → 事务外执行 + 心跳续租 → fencing 条件提交；崩溃不写终态、租约到期重领取。
type Worker struct {
	store     Store
	owner     string
	tenantID  string
	leaseFor  time.Duration
	heartbeat time.Duration
	poll      time.Duration
	handler   HandlerFunc
	backoff   func(attempt int) time.Duration
	logger    *log.Logger
}

// WorkerOptions Worker 构造参数（零值给默认）。
type WorkerOptions struct {
	LeaseFor  time.Duration // 租约时长（默认 30s）
	Heartbeat time.Duration // 心跳间隔（默认 lease/3）
	Poll      time.Duration // 无任务轮询间隔（默认 500ms）
	Backoff   func(attempt int) time.Duration
	Logger    *log.Logger
}

// NewWorker 创建 worker。
func NewWorker(store Store, owner, tenantID string, handler HandlerFunc, opts WorkerOptions) *Worker {
	w := &Worker{store: store, owner: owner, tenantID: tenantID, handler: handler}
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
	w.logger = opts.Logger
	if w.logger == nil {
		w.logger = log.Default()
	}
	return w
}

// Run 进入领取循环，直到 ctx 取消或不可恢复错误。
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := w.store.ClaimNext(ctx, w.tenantID, w.owner, w.leaseFor)
		switch {
		case err == nil:
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

// process 执行单任务：心跳驱动 handler，取消时不做终态提交（留给租约过期重领取）。
func (w *Worker) process(ctx context.Context, job *Job) {
	// 注入任务所属租户，供续租/终态提交与 handler 建立 RLS 上下文。
	ctx = tenant.WithContext(ctx, job.TenantID)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	hbDone := make(chan struct{})
	go w.heartbeatRun(workCtx, job, hbDone)

	err := w.handler(workCtx, job)
	cancel()
	<-hbDone

	// 父 ctx 取消 = worker 停机/崩溃：不写终态，靠租约过期让其他 worker 重领取。
	if ctx.Err() != nil {
		w.logger.Printf("worker: ctx canceled for job %s, leaving for reclaim", job.ID)
		return
	}
	if err != nil {
		if retry := AsRetry(err); retry != nil {
			at := retry.At
			if at.IsZero() {
				at = time.Now().Add(w.backoff(job.Attempt))
			}
			if jerr := w.store.ScheduleRetry(ctx, job.ID, job.LeaseOwner, job.FencingToken, at, marshalRetryError(retry, at)); jerr != nil {
				w.logger.Printf("worker: schedule retry failed job=%s: %v", job.ID, jerr)
			}
			w.logger.Printf("worker: job %s scheduled retry at %s", job.ID, at)
			return
		}
		if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateFailed, TryMarshalJobError(err)); cerr != nil {
			w.logger.Printf("worker: complete failed job=%s: %v", job.ID, cerr)
		}
		return
	}
	if cerr := w.store.Complete(ctx, job.ID, job.LeaseOwner, job.FencingToken, StateSucceeded, nil); cerr != nil {
		w.logger.Printf("worker: complete succeeded job=%s: %v", job.ID, cerr)
	}
}

// heartbeatRun 周期性续租；fencing 失效时通知取消 handler。
func (w *Worker) heartbeatRun(ctx context.Context, job *Job, done chan<- struct{}) {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
