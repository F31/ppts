//go:build pg

package pipeline

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
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
	_, _ = s.Create(ctx, testTenant, testProject, string(KindParse), "w-retry", "snap", time.Time{})

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
	jr, err := s.lookup(ctx, testTenant, "w-retry", string(KindParse))
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
