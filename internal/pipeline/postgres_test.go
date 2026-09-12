//go:build pg

// PG 端到端测试：验证任务领取/租约/fencing/恢复/幂等。
// 需要环境变量 PPTS_TEST_DATABASE（postgres://user:pass@host:port/db），
// 库内已应用 migrations/0001_init.sql。未设置时测试整体跳过。
package pipeline

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/F31/ppts/internal/tenant"
)

const (
	testTenant       = "00000000-0000-0000-0000-000000000001"
	testProject      = "00000000-0000-0000-0000-000000000101"
	testOtherTenant  = "00000000-0000-0000-0000-000000000002"
	testOtherProject = "00000000-0000-0000-0000-000000000102"
)

func testStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set; skipping PG pipeline tests")
	}
	s, err := NewPGStore(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	t.Cleanup(s.Close)
	// 隔离测试数据。
	if _, err := s.pool.Exec(context.Background(),
		"TRUNCATE jobs, job_steps, source_revisions, projects, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 满足外键：插入测试租户与项目（项目写入需租户上下文，受 RLS 约束）。
	for _, tkt := range []struct{ t, p string }{
		{testTenant, testProject}, {testOtherTenant, testOtherProject},
	} {
		if _, err := s.pool.Exec(context.Background(),
			"INSERT INTO tenants(id,name) VALUES ($1::uuid,$2) ON CONFLICT DO NOTHING",
			tkt.t, "test-tenant"); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
		if err := tenant.Run(context.Background(), s.pool, tkt.t, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1::uuid,$2::uuid,$3,$3) ON CONFLICT DO NOTHING",
				tkt.p, tkt.t, "tester")
			return err
		}); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	}
	return s
}

func TestCreateIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	runAt := time.Time{}
	j1, err := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-1", "snap-1", runAt)
	if err != nil {
		t.Fatalf("Create#1: %v", err)
	}
	j2, err := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-1", "snap-1", runAt)
	if err != nil {
		t.Fatalf("Create#2: %v", err)
	}
	if j1.ID != j2.ID {
		t.Fatalf("idempotent create returned different ids: %s vs %s", j1.ID, j2.ID)
	}
	if j1.State != StateQueued || j1.Attempt != 0 {
		t.Fatalf("job: %+v", j1)
	}
}

func TestClaimHeartbeatComplete(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, err := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-c1", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := s.ClaimNext(ctx, testTenant, "worker-a", 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != j.ID || claimed.Attempt != 1 || claimed.LeaseOwner != "worker-a" ||
		claimed.FencingToken != 1 || claimed.State != StateRunning {
		t.Fatalf("claimed: %+v", claimed)
	}
	if err := s.Heartbeat(ctx, claimed.ID, "worker-a", 1, 30*time.Second); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := s.Complete(ctx, claimed.ID, "worker-a", 1, StateSucceeded, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSucceeded || got.Progress != 100 {
		t.Fatalf("after complete: %+v", got)
	}
	// 已终态不可再领取。
	_, err = s.ClaimNext(ctx, testTenant, "worker-b", time.Second)
	if !errors.Is(err, ErrNoJob) {
		t.Fatalf("claim after terminal: want ErrNoJob, got %v", err)
	}
}

func TestStaleWorkerFencingRejected(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-f", "snap", time.Time{})
	claimed, err := s.ClaimNext(ctx, testTenant, "worker-a", 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if claimed.ID != j.ID {
		t.Fatalf("claimed id mismatch: %s vs %s", claimed.ID, j.ID)
	}
	// 租约在 worker-a 且 fencing=1；用错误 fencing 提交 → 无效。
	if err := s.Complete(ctx, j.ID, "worker-a", 999, StateSucceeded, nil); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("wrong fencing: got %v, want ErrLeaseMismatch", err)
	}
	// 换 owner 提交（worker-b 冒充）→ 无效。
	if err := s.Complete(ctx, j.ID, "worker-b", 1, StateSucceeded, nil); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("wrong owner: got %v, want ErrLeaseMismatch", err)
	}
	// 任务仍在运行态，未被篡改。
	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateRunning {
		t.Fatalf("job state changed by stale worker: %+v", got)
	}
}

func TestLeaseExpiryReclaim(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-L", "snap", time.Time{})
	if _, err := s.ClaimNext(ctx, testTenant, "worker-a", 30*time.Millisecond); err != nil {
		t.Fatalf("ClaimNext a: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	// 租约过期，worker-b 重新领取：attempt/fencing 递增，owner 转移。
	claimed, err := s.ClaimNext(ctx, testTenant, "worker-b", 30*time.Second)
	if err != nil {
		t.Fatalf("reclaim after expiry: %v", err)
	}
	if claimed.ID != j.ID || claimed.Attempt != 2 || claimed.FencingToken != 2 || claimed.LeaseOwner != "worker-b" {
		t.Fatalf("reclaimed: %+v", claimed)
	}
	// worker-a（旧租约）迟到提交被拒。
	if err := s.Complete(ctx, j.ID, "worker-a", 1, StateSucceeded, nil); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("stale complete: got %v, want ErrLeaseMismatch", err)
	}
}

func TestRetrySchedule(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-R", "snap", time.Time{})
	if _, err := s.ClaimNext(ctx, testTenant, "worker-a", 30*time.Second); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := s.ScheduleRetry(ctx, j.ID, "worker-a", 1, future, []byte(`{"code":"throttled","retryable":true}`)); err != nil {
		t.Fatalf("ScheduleRetry: %v", err)
	}
	// run_at 在未来：领取应失败。
	if _, err := s.ClaimNext(ctx, testTenant, "worker-b", time.Second); !errors.Is(err, ErrNoJob) {
		t.Fatalf("claim before run_at: got %v, want ErrNoJob", err)
	}
	// 把 run_at 改为过去（模拟退避到期）→ 可领取。
	if err := tenant.Run(ctx, s.pool, testTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE jobs SET run_at=now()-interval '1 minute' WHERE id=$1", j.ID)
		return err
	}); err != nil {
		t.Fatalf("bump run_at: %v", err)
	}
	claimed, err := s.ClaimNext(ctx, testTenant, "worker-b", 30*time.Second)
	if err != nil || claimed.State != StateRunning {
		t.Fatalf("reclaim after run_at: %v, j=%+v", err, claimed)
	}
	if claimed.LastError == nil || !claimed.LastError.Retryable {
		t.Fatalf("last_error preserved: %+v", claimed.LastError)
	}
}

func TestCancelQueuedJob(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-C", "snap", time.Time{})
	c, err := s.Cancel(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// queued 直接取消。
	if c == nil || c.State != StateCanceled {
		t.Fatalf("Cancel: %+v", c)
	}
	if _, err := s.ClaimNext(ctx, testTenant, "worker-x", time.Second); !errors.Is(err, ErrNoJob) {
		t.Fatalf("claim after cancel: got %v", err)
	}
	// 不可取消状态（已取消）返回 ErrJobNotCancelable。
	if _, err := s.Cancel(ctx, j.ID, testTenant); !errors.Is(err, ErrJobNotCancelable) {
		t.Fatalf("cancel terminal: got %v want ErrJobNotCancelable", err)
	}
	// 不存在的任务返回 ErrJobNotFound。
	if _, err := s.Cancel(ctx, "00000000-0000-0000-0000-0000000000ff", testTenant); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("cancel missing: got %v want ErrJobNotFound", err)
	}
}

func TestCancelRunningAndHeartbeatDetectsRequest(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-RC", "snap", time.Time{})
	if _, err := s.ClaimNext(ctx, testTenant, "worker-a", 30*time.Second); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	c, err := s.Cancel(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Cancel running: %v", err)
	}
	if c.State != StateCancelReq {
		t.Fatalf("running cancel state = %s want cancel_requested", c.State)
	}
	// 心跳应发现取消请求。
	if err := s.Heartbeat(ctx, j.ID, "worker-a", 1, 30*time.Second); !errors.Is(err, ErrCancelRequested) {
		t.Fatalf("heartbeat: got %v want ErrCancelRequested", err)
	}
	// worker 安全点停止后提交 canceled。
	if err := s.Complete(ctx, j.ID, "worker-a", 1, StateCanceled, nil); err != nil {
		t.Fatalf("Complete canceled: %v", err)
	}
	got, err := s.Get(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateCanceled {
		t.Fatalf("state = %s want canceled", got.State)
	}
}

func TestRetryFailedJob(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-RF", "snap", time.Time{})
	if _, err := s.ClaimNext(ctx, testTenant, "worker-a", 30*time.Second); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if err := s.Complete(ctx, j.ID, "worker-a", 1, StateFailed, []byte(`{"code":"internal"}`)); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	retried, err := s.RetryFailed(ctx, j.ID, testTenant)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if retried.State != StateQueued || retried.LastError != nil {
		t.Fatalf("retried = %+v", retried)
	}
	// 非 failed 状态不可重试。
	if _, err := s.RetryFailed(ctx, j.ID, testTenant); !errors.Is(err, ErrJobNotRetryable) {
		t.Fatalf("retry non-failed: got %v want ErrJobNotRetryable", err)
	}
}

func TestListJobsFiltersAndPaginates(t *testing.T) {
	s := testStore(t)
	ctx := tenant.WithContext(context.Background(), testTenant)
	for _, key := range []string{"l-1", "l-2", "l-3"} {
		if _, err := s.Create(ctx, testTenant, testProject, string(KindParse), key, "snap", time.Time{}); err != nil {
			t.Fatalf("Create %s: %v", key, err)
		}
	}
	jobs, next, err := s.List(ctx, testTenant, testProject, "", "", 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 || next == "" {
		t.Fatalf("page1 jobs=%d next=%q", len(jobs), next)
	}
	jobs2, next2, err := s.List(ctx, testTenant, testProject, "", next, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(jobs2) != 1 || next2 != "" {
		t.Fatalf("page2 jobs=%d next=%q", len(jobs2), next2)
	}
	// 状态过滤：全部 queued。
	queued, _, err := s.List(ctx, testTenant, testProject, string(StateQueued), "", 10)
	if err != nil {
		t.Fatalf("List queued: %v", err)
	}
	if len(queued) != 3 {
		t.Fatalf("queued jobs = %d want 3", len(queued))
	}
	succeeded, _, err := s.List(ctx, testTenant, testProject, string(StateSucceeded), "", 10)
	if err != nil {
		t.Fatalf("List succeeded: %v", err)
	}
	if len(succeeded) != 0 {
		t.Fatalf("succeeded jobs = %d want 0", len(succeeded))
	}
}

func TestCrossTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	j, _ := s.Create(ctx, testTenant, testProject, string(KindParse), "idem-X", "snap", time.Time{})
	// 其他租户领取不到，也查不到。
	if _, err := s.ClaimNext(ctx, testOtherTenant, "worker-y", time.Second); !errors.Is(err, ErrNoJob) {
		t.Fatalf("cross tenant claim: got %v", err)
	}
	if _, err := s.Get(ctx, j.ID, testOtherTenant); err == nil {
		t.Fatalf("cross tenant Get should fail")
	}
}
