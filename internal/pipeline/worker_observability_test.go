package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

type observeStore struct {
	completed JobState
	retried   bool
}

func (s *observeStore) Create(context.Context, string, string, string, string, string, time.Time) (*Job, error) {
	return nil, errors.New("not implemented")
}
func (s *observeStore) ClaimNext(context.Context, string, string, time.Duration) (*Job, error) {
	return nil, ErrNoJob
}
func (s *observeStore) Heartbeat(context.Context, string, string, int64, time.Duration) error {
	return nil
}
func (s *observeStore) Complete(_ context.Context, _ string, _ string, _ int64, state JobState, _ []byte) error {
	s.completed = state
	return nil
}
func (s *observeStore) ScheduleRetry(context.Context, string, string, int64, time.Time, []byte) error {
	s.retried = true
	return nil
}
func (s *observeStore) MarkStep(context.Context, JobStep) error { return nil }
func (s *observeStore) Cancel(context.Context, string, string) (*Job, error) {
	return nil, ErrJobNotFound
}
func (s *observeStore) RetryFailed(context.Context, string, string) (*Job, error) {
	return nil, ErrJobNotFound
}
func (s *observeStore) List(context.Context, string, string, string, string, int) ([]*Job, string, error) {
	return nil, "", nil
}
func (s *observeStore) Get(context.Context, string, string) (*Job, error) { return nil, ErrJobNotFound }

type observeMetrics struct {
	completed []JobState
	retries   int
}

func (m *observeMetrics) JobClaimed(*Job) {}
func (m *observeMetrics) JobCompleted(_ *Job, state JobState, _ time.Duration) {
	m.completed = append(m.completed, state)
}
func (m *observeMetrics) JobRetryScheduled(*Job)  { m.retries++ }
func (m *observeMetrics) JobCancelRequested(*Job) {}
func (m *observeMetrics) JobLeaseLost(*Job)       {}

func TestWorkerMetricsSuccessAndRetry(t *testing.T) {
	job := &Job{ID: "j1", TenantID: "tenant-1", ProjectID: "project-1", Kind: KindParse, LeaseOwner: "wk", FencingToken: 1}

	metrics := &observeMetrics{}
	store := &observeStore{}
	w := NewWorker(store, "wk", "tenant-1", func(context.Context, *Job) error { return nil }, WorkerOptions{Metrics: metrics})
	w.process(context.Background(), job)
	if store.completed != StateSucceeded || len(metrics.completed) != 1 || metrics.completed[0] != StateSucceeded {
		t.Fatalf("success metrics/store = completed %v metrics %v", store.completed, metrics.completed)
	}

	metrics = &observeMetrics{}
	store = &observeStore{}
	w = NewWorker(store, "wk", "tenant-1", func(context.Context, *Job) error {
		return &RetryError{Err: errors.New("rate limited"), At: time.Now().Add(time.Minute)}
	}, WorkerOptions{Metrics: metrics})
	w.process(context.Background(), job)
	if !store.retried || metrics.retries != 1 || len(metrics.completed) != 0 {
		t.Fatalf("retry metrics/store = retried %v retries %d completed %v", store.retried, metrics.retries, metrics.completed)
	}
}

func TestWorkerCallsOnCanceledAfterTerminalCancel(t *testing.T) {
	job := &Job{
		ID: "j-cancel", TenantID: "tenant-1", ProjectID: "project-1", Kind: KindNarration,
		State: StateCancelReq, LeaseOwner: "wk", FencingToken: 1,
	}
	store := &observeStore{}
	called := false
	w := NewWorker(store, "wk", "tenant-1", func(context.Context, *Job) error {
		t.Fatal("handler should not run for cancel_requested job")
		return nil
	}, WorkerOptions{OnCanceled: func(context.Context, *Job) error {
		called = true
		return nil
	}})
	w.process(context.Background(), job)
	if store.completed != StateCanceled || !called {
		t.Fatalf("completed=%s onCanceled=%v", store.completed, called)
	}
}
