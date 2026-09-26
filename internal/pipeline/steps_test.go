package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
)

// TestStepOverwrites 固定步骤状态覆盖规则的判定表。
//
// 关键的一条是 failed → pending 必须为 true：任务重试时重跑失败过的步骤依赖它，
// 任何"数值越大越优先"的单调实现都会在这里把重试卡死。
func TestStepOverwrites(t *testing.T) {
	cases := []struct {
		cur, next JobStepState
		want      bool
	}{
		{StepPending, StepPending, true},
		{StepSuccess, StepPending, false},  // 已完成的事实不能被"未开始"抹掉
		{StepSkipped, StepPending, false},  // 跳过也是结论
		{StepDegraded, StepPending, false}, // 降级产出也是结论
		{StepFailed, StepPending, true},    // 重试必须允许重跑
		{StepSuccess, StepFailed, true},    // 失败是事实，可覆盖成功
		{StepFailed, StepSuccess, true},
		{StepDegraded, StepSuccess, true},
		{StepSuccess, StepDegraded, true},
		{StepPending, StepSuccess, true},
	}
	for _, c := range cases {
		if got := stepOverwrites(c.cur, c.next); got != c.want {
			t.Errorf("stepOverwrites(%s, %s) = %v, want %v", c.cur, c.next, got, c.want)
		}
	}
}

// TestStepOverwriteCondCoversSettledStates 防止新增状态漏登记：
// 条件文本由 stepStates + stepStateSettled 推导，漏登记的状态会被当成"未下结论"，
// 于是允许被 pending 抹掉——这正是本规则要挡的事，必须让漏登记立刻变红。
func TestStepOverwriteCondCoversSettledStates(t *testing.T) {
	for _, d := range []dialect{dialectPG, dialectSQLite} {
		cond := stepOverwriteCond(d)
		for _, s := range stepStates {
			if !stepStateProductive(s) {
				continue
			}
			if !strings.Contains(cond, "'"+string(s)+"'") {
				t.Errorf("dialect=%d cond missing productive state %q: %s", d, s, cond)
			}
		}
		if !strings.Contains(cond, ".state <> 'pending'") {
			t.Errorf("dialect=%d cond does not gate on pending: %s", d, cond)
		}
	}
}

// TestSQLiteMarkStepPendingKeepsSettled 用真实 SQLite 验证：重跑开场写的 pending
// 不会把已完成的步骤抹回"未开始"，也不会顺带把 phase 改掉。
func TestSQLiteMarkStepPendingKeepsSettled(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)
	const tenant = db.LocalTenantID

	j, err := s.Create(ctx, tenant, "p", "parse", "idem-step-keep", `{}`, time.Time{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimNext(ctx, tenant, "w1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	lctx := WithStepLease(ctx, claimed.LeaseOwner, claimed.FencingToken)

	mark := func(state JobStepState, ref string) error {
		return s.MarkStep(lctx, JobStep{
			JobID: claimed.ID, TenantID: tenant, StepType: "pages", StepKey: "k1",
			State: state, ResultRef: ref,
		})
	}
	stepsOf := func() []JobStep {
		t.Helper()
		got, err := s.ListSteps(ctx, tenant, claimed.ID)
		if err != nil {
			t.Fatalf("list steps: %v", err)
		}
		return got
	}
	phaseOf := func() string {
		t.Helper()
		got, err := s.Get(ctx, j.ID, tenant)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		row, err := s.db.QueryContext(ctx, `SELECT phase FROM jobs WHERE id = ?`, j.ID)
		if err != nil {
			t.Fatalf("query phase: %v", err)
		}
		defer row.Close()
		var phase string
		for row.Next() {
			if err := row.Scan(&phase); err != nil {
				t.Fatalf("scan phase: %v", err)
			}
		}
		_ = got
		return phase
	}

	if err := mark(StepSuccess, "ref-success"); err != nil {
		t.Fatalf("mark success: %v", err)
	}
	// 重跑开场：export.go / narration.go 都会先写 pending。
	if err := mark(StepPending, ""); err != nil {
		t.Fatalf("mark pending: %v", err)
	}
	got := stepsOf()
	if len(got) != 1 || got[0].State != StepSuccess {
		t.Fatalf("pending must not erase success: %+v", got)
	}
	if got[0].ResultRef != "ref-success" {
		t.Fatalf("result_ref must survive rejected write: %q", got[0].ResultRef)
	}
	if p := phaseOf(); p != "pages" {
		t.Fatalf("phase after rejected write = %q, want pages", p)
	}

	// 失败是事实，可以覆盖成功。
	if err := mark(StepFailed, ""); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if got = stepsOf(); got[0].State != StepFailed {
		t.Fatalf("failed must override success: %+v", got)
	}
	// 重试重跑：pending 可以覆盖 failed。
	if err := mark(StepPending, ""); err != nil {
		t.Fatalf("mark pending after failed: %v", err)
	}
	if got = stepsOf(); got[0].State != StepPending {
		t.Fatalf("pending must be able to retry a failed step: %+v", got)
	}
}

// TestSQLiteMarkStepLeaseGuard 验证租约守卫：过期 worker 与已终态任务都写不进步骤。
func TestSQLiteMarkStepLeaseGuard(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)
	const tenant = db.LocalTenantID

	if _, err := s.Create(ctx, tenant, "p", "parse", "idem-step-lease", `{}`, time.Time{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimNext(ctx, tenant, "w1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	step := JobStep{
		JobID: claimed.ID, TenantID: tenant, StepType: "pages", StepKey: "k1",
		State: StepSuccess, ResultRef: "r1",
	}
	lctx := WithStepLease(ctx, claimed.LeaseOwner, claimed.FencingToken)
	if err := s.MarkStep(lctx, step); err != nil {
		t.Fatalf("mark with valid lease: %v", err)
	}

	// 过期 fencing（已被他人重领）：写入必须被拒。
	if err := s.MarkStep(WithStepLease(ctx, claimed.LeaseOwner, claimed.FencingToken+1), step); err != ErrLeaseMismatch {
		t.Fatalf("stale fencing should be rejected, got %v", err)
	}
	// 他人持有租约：同样被拒。
	if err := s.MarkStep(WithStepLease(ctx, "w2", claimed.FencingToken), step); err != ErrLeaseMismatch {
		t.Fatalf("foreign owner should be rejected, got %v", err)
	}
	// 未声明租约（零值）：保持放行，历史调用方不受影响。
	if err := s.MarkStep(ctx, step); err != nil {
		t.Fatalf("lease-less write must stay allowed: %v", err)
	}

	// 终态任务（Complete 已把 lease_owner 置 NULL）：带凭据的写入一律被拒。
	if err := s.Complete(ctx, claimed.ID, claimed.LeaseOwner, claimed.FencingToken, StateSucceeded, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}
	step.State = StepPending
	if err := s.MarkStep(lctx, step); err != ErrLeaseMismatch {
		t.Fatalf("write to terminal job should be rejected, got %v", err)
	}
}

// TestWorkerInjectsStepLease 验证 worker 在执行入口注入租约凭据——
// handler 里的十几处 MarkStep 全靠这一次注入获得守卫，注入断了守卫就是摆设。
func TestWorkerInjectsStepLease(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)
	const tenant = db.LocalTenantID

	if _, err := s.Create(ctx, tenant, "p", "parse", "idem-worker-lease", `{}`, time.Time{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimNext(ctx, tenant, "w1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	seen := false
	w := NewWorker(s, "w1", tenant, func(hctx context.Context, job *Job) error {
		owner, fencing := resolveStepLease(hctx, JobStep{})
		if owner != job.LeaseOwner || fencing != job.FencingToken {
			t.Errorf("handler lease = (%q,%d), want (%q,%d)", owner, fencing, job.LeaseOwner, job.FencingToken)
		}
		// 真实写一次：不带显式凭据，应凭上下文通过校验。
		if err := s.MarkStep(hctx, JobStep{
			JobID: job.ID, TenantID: job.TenantID, StepType: "pages", StepKey: "k1",
			State: StepSuccess, ResultRef: "r",
		}); err != nil {
			t.Errorf("mark step via worker ctx: %v", err)
		}
		seen = true
		return nil
	}, WorkerOptions{})
	w.process(ctx, claimed)
	if !seen {
		t.Fatal("handler never ran")
	}
}
