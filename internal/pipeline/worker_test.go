//go:build pg

package pipeline

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/F31/ppts/internal/tenant"
)

func TestWorkerSuccessAndRetryThenFailed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := true
	w := NewWorker(s, "wk", testTenant, func(ctx context.Context, job *Job) error {
		if job.IDempotencyKey == "w-retry" && first {
			first = false
			return &RetryError{Err: errors.New("throttled"), At: time.Now().Add(50 * time.Millisecond)}
		}
		return nil
	}, WorkerOptions{Poll: 10 * time.Millisecond})

	j1, err := s.Create(ctx, testTenant, testProject, string(KindParse), "w-ok", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create ok: %v", err)
	}
	jRetry, err := s.Create(ctx, testTenant, testProject, string(KindParse), "w-retry", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create retry: %v", err)
	}

	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = w.Run(runCtx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run: %v", err)
	}
	// 首个任务应已 succeeded（在超时窗口内完成）。
	got, err := s.Get(ctx, j1.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded {
		t.Fatalf("ok job terminal: got %s", got.State)
	}
	// 重试任务：第一次触发退避，第二次成功（attempt=2）。
	jr, err := s.Get(ctx, jRetry.ID, testTenant)
	if err != nil {
		t.Fatalf("lookup retry: %v", err)
	}
	if jr.State != StateSucceeded || jr.Attempt != 2 {
		t.Fatalf("retry job: state=%s attempt=%d", jr.State, jr.Attempt)
	}
}

func TestWorkerRetryRateLimited(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	w := NewWorker(s, "wk", testTenant, func(ctx context.Context, job *Job) error {
		return &RetryError{Err: errors.New("429"), At: time.Now().Add(time.Hour)}
	}, WorkerOptions{Poll: 10 * time.Millisecond})

	j, err := s.Create(ctx, testTenant, testProject, string(KindParse), "w-429", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = w.Run(runCtx) // 时间到退出即可

	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateRetryWait || got.Attempt != 1 {
		t.Fatalf("after 429 retry: state=%s attempt=%d", got.State, got.Attempt)
	}
	if got.LastError == nil || !got.LastError.Retryable {
		t.Fatalf("retryable error recorded: %+v", got.LastError)
	}
}

func TestWorkerPermanentFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	w := NewWorker(s, "wk", testTenant, func(ctx context.Context, job *Job) error {
		return &retryableHTTPErr{code: "invalid_argument", status: http.StatusBadRequest}
	}, WorkerOptions{Poll: 10 * time.Millisecond})

	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "w-fail", "snap", time.Time{})
	runCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = w.Run(runCtx)

	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateFailed || got.Attempt != 1 {
		t.Fatalf("permanent fail: state=%s attempt=%d", got.State, got.Attempt)
	}
}

func TestWorkerUnknownProviderResultCanBeRetried(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	w := NewWorker(s, "wk", testTenant, func(ctx context.Context, job *Job) error {
		return &UnknownResultError{Err: errors.New("provider timeout after submit")}
	}, WorkerOptions{Poll: 10 * time.Millisecond})

	j, _ := s.Create(ctx, testTenant, testProject, string(KindNarration), "w-unknown", "snap", time.Time{})
	runCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = w.Run(runCtx)

	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateUnknownResult || got.Attempt != 1 {
		t.Fatalf("unknown result: state=%s attempt=%d", got.State, got.Attempt)
	}
	if got.LastError == nil || got.LastError.Message == "" {
		t.Fatalf("unknown result error not recorded: %+v", got.LastError)
	}
	retried, err := s.RetryFailed(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("RetryFailed unknown: %v", err)
	}
	if retried.State != StateQueued || retried.LastError != nil {
		t.Fatalf("retried unknown: state=%s last_error=%+v", retried.State, retried.LastError)
	}
}

func TestWorkerCrashReclaimNoLoss(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	started := make(chan struct{}, 4)
	releases := make(chan struct{}, 4)
	handler := func(ctx context.Context, job *Job) error {
		started <- struct{}{}
		select {
		case <-releases:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w := NewWorker(s, "wk-crash", testTenant, handler, WorkerOptions{
		LeaseFor: 150 * time.Millisecond, Heartbeat: 40 * time.Millisecond, Poll: 10 * time.Millisecond,
	})

	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "w-crash", "snap", time.Time{})

	// 第一次运行：handler 挂起直到 ctx 取消（模拟 worker 崩溃：Run 退出，不写终态）。
	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	<-started // handler 已开始
	select {
	case <-started:
		t.Fatal("unexpected second claim")
	default:
	}
	cancel() // 模拟崩溃
	<-done

	time.Sleep(200 * time.Millisecond) // 等租约过期

	// 第二次运行：新 worker 重领取同一任务，attempt/fencing 递增，最终 succeeded。
	sawClaim := false
	w2 := NewWorker(s, "wk2", testTenant, func(ctx context.Context, job *Job) error {
		sawClaim = true
		releases <- struct{}{}
		if job.Attempt != 2 {
			t.Fatalf("reclaim attempt: got %d want 2", job.Attempt)
		}
		return nil
	}, WorkerOptions{LeaseFor: 3 * time.Second, Heartbeat: time.Second, Poll: 10 * time.Millisecond})
	runCtx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	_ = w2.Run(runCtx2)

	if !sawClaim {
		t.Fatalf("job never reclaimed after crash")
	}
	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded {
		t.Fatalf("after reclaim: state=%s", got.State)
	}
}

// retryableHTTPErr 模拟供应商类可分类错误。
type retryableHTTPErr struct {
	code   string
	status int
}

func (e *retryableHTTPErr) Error() string   { return e.code }
func (e *retryableHTTPErr) HTTPStatus() int { return e.status }

// TestWorkerCrashAfterStepReplayIdempotent 覆盖崩溃窗口：步骤写入成功后 worker 强杀，
// 新 worker 重放同一任务；步骤幂等（单行/引用不变），任务最终恰好成功一次。
func TestWorkerCrashAfterStepReplayIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "w-step-crash", "snap", time.Time{})

	stepCalls := 0
	started := make(chan struct{}, 1)
	handler := func(ctx context.Context, job *Job) error {
		stepCalls++
		if err := s.MarkStep(ctx, JobStep{
			TenantID: job.TenantID, JobID: job.ID, StepType: "parse", StepKey: "extract",
			State: "success", ResultRef: "ref-1",
		}); err != nil {
			return err
		}
		if stepCalls == 1 {
			<-ctx.Done() // 模拟步骤成功后崩溃：不写终态、不续租
			return ctx.Err()
		}
		return nil
	}
	w1 := NewWorker(s, "wk-step1", testTenant, func(ctx context.Context, job *Job) error {
		started <- struct{}{}
		return handler(ctx, job)
	}, WorkerOptions{LeaseFor: 150 * time.Millisecond, Heartbeat: 40 * time.Millisecond, Poll: 10 * time.Millisecond})

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	done := make(chan error, 1)
	go func() { done <- w1.Run(runCtx) }()
	<-started
	cancel()
	<-done
	time.Sleep(200 * time.Millisecond) // 等租约过期

	w2 := NewWorker(s, "wk-step2", testTenant, handler, WorkerOptions{
		LeaseFor: 3 * time.Second, Heartbeat: time.Second, Poll: 10 * time.Millisecond,
	})
	runCtx2, cancel2 := context.WithTimeout(ctx, 2*time.Second)
	defer cancel2()
	if err := w2.Run(runCtx2); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run w2: %v", err)
	}

	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded || got.Attempt != 2 {
		t.Fatalf("after replay: state=%s attempt=%d", got.State, got.Attempt)
	}
	if stepCalls != 2 {
		t.Fatalf("handler ran %d times want 2 (replay)", stepCalls)
	}
	if ref, err := s.StepResultRef(ctx, j.ID, "parse"); err != nil || ref != "ref-1" {
		t.Fatalf("step result after replay = %q err=%v", ref, err)
	}
}
