package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newPipelineStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("fs: %v", err)
	}
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

func TestSQLiteJobLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)
	const tenant = db.LocalTenantID
	const project = "proj-1"

	// Create（幂等）
	j, err := s.Create(ctx, tenant, project, "parse", "idem-1", `{}`, time.Time{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	again, err := s.Create(ctx, tenant, project, "parse", "idem-1", `{}`, time.Time{})
	if err != nil || again.ID != j.ID {
		t.Fatalf("idempotent create: err=%v id=%s want=%s", err, again.ID, j.ID)
	}
	if j.State != StateQueued {
		t.Fatalf("want queued, got %s", j.State)
	}

	// ClaimNext
	claimed, err := s.ClaimNext(ctx, tenant, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.State != StateRunning || claimed.Attempt != 1 || claimed.FencingToken != 1 {
		t.Fatalf("unexpected claimed job: %+v", claimed)
	}
	// 再次领取应无任务（已租约）
	if _, err := s.ClaimNext(ctx, tenant, "worker-2", time.Minute); err != ErrNoJob {
		t.Fatalf("second claim should be no-job, got %v", err)
	}

	// Heartbeat
	if err := s.Heartbeat(ctx, claimed.ID, "worker-1", claimed.FencingToken, time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	// 错误 fencing → ErrLeaseMismatch
	if err := s.Heartbeat(ctx, claimed.ID, "worker-1", claimed.FencingToken+99, time.Minute); err != ErrLeaseMismatch {
		t.Fatalf("bad fencing heartbeat should mismatch, got %v", err)
	}

	// UpdateProgress
	if err := s.UpdateProgress(ctx, claimed.ID, "worker-1", claimed.FencingToken, 42); err != nil {
		t.Fatalf("progress: %v", err)
	}

	// MarkStep（维护 phase）
	if err := s.MarkStep(ctx, JobStep{
		JobID: claimed.ID, TenantID: tenant, StepType: "pages", StepKey: "k1", State: StepSuccess,
	}); err != nil {
		t.Fatalf("mark step: %v", err)
	}

	// Complete
	if err := s.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateSucceeded, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, err := s.Get(ctx, claimed.ID, tenant)
	if err != nil || got.State != StateSucceeded || got.Progress != 100 {
		t.Fatalf("after complete: err=%v job=%+v", err, got)
	}

	// 事件流（应含 queued/running/progress/succeeded）
	events, err := s.EventsSince(ctx, tenant, project, 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected >=3 events, got %d", len(events))
	}

	// ListPage + PhaseCounts
	rows, _, err := s.ListPage(ctx, tenant, JobFilter{ProjectID: project, Sort: "created"}, "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list page: err=%v n=%d", err, len(rows))
	}
	counts, err := s.PhaseCounts(ctx, tenant, project)
	if err != nil || counts["pages"] != 1 {
		t.Fatalf("phase counts: err=%v counts=%v", err, counts)
	}

	// ListSteps
	steps, err := s.ListSteps(ctx, tenant, claimed.ID)
	if err != nil || len(steps) != 1 || steps[0].State != StepSuccess {
		t.Fatalf("list steps: err=%v steps=%+v", err, steps)
	}
}

func TestSQLiteJobCancelAndRetry(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)
	const tenant = db.LocalTenantID

	// queued → canceled
	j, _ := s.Create(ctx, tenant, "p", "parse", "idem-c", `{}`, time.Time{})
	canceled, err := s.Cancel(ctx, j.ID, tenant)
	if err != nil || canceled.State != StateCanceled {
		t.Fatalf("cancel queued: err=%v state=%s", err, canceled.State)
	}

	// running → cancel_requested
	j2, _ := s.Create(ctx, tenant, "p", "parse", "idem-r", `{}`, time.Time{})
	claimed, err := s.ClaimNext(ctx, tenant, "w", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	req, err := s.Cancel(ctx, claimed.ID, tenant)
	if err != nil || req.State != StateCancelReq {
		t.Fatalf("cancel running: err=%v state=%s", err, req.State)
	}
	// Heartbeat 应返回 ErrCancelRequested
	if err := s.Heartbeat(ctx, claimed.ID, "w", claimed.FencingToken, time.Minute); err != ErrCancelRequested {
		t.Fatalf("heartbeat after cancel should signal cancel, got %v", err)
	}
	_ = j2

	// failed → retry
	j3, _ := s.Create(ctx, tenant, "p", "parse", "idem-f", `{}`, time.Time{})
	c, _ := s.ClaimNext(ctx, tenant, "w", time.Minute)
	if err := s.Complete(ctx, c.ID, "w", c.FencingToken, StateFailed, []byte(`{"message":"boom"}`)); err != nil {
		t.Fatalf("fail complete: %v", err)
	}
	retried, err := s.RetryFailed(ctx, j3.ID, tenant)
	if err != nil || retried.State != StateQueued {
		t.Fatalf("retry: err=%v state=%s", err, retried.State)
	}
}

func TestSQLiteJobScheduleRetry(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)
	const tenant = db.LocalTenantID

	s.Create(ctx, tenant, "p", "parse", "idem-sr", `{}`, time.Time{})
	c, err := s.ClaimNext(ctx, tenant, "w", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := s.ScheduleRetry(ctx, c.ID, "w", c.FencingToken, future, []byte(`{"message":"retry"}`)); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	got, _ := s.Get(ctx, c.ID, tenant)
	if got.State != StateRetryWait {
		t.Fatalf("want retry_wait, got %s", got.State)
	}
	// run_at 未到 → 不可领取
	if _, err := s.ClaimNext(ctx, tenant, "w2", time.Minute); err != ErrNoJob {
		t.Fatalf("future run_at should not be claimable, got %v", err)
	}
}
