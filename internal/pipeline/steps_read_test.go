//go:build pg

// B4-M6a 的 PG 端到端测试：任务步骤读取/聚合与批量任务查询。
// 与 postgres_test.go 同构建标签与 helper（testStore/testTenant 等）。
// 需要 PPTS_TEST_DATABASE；未设置时 testStore 直接 t.Skip（仓库惯例：环境依赖型测试跳过而非失败）。
package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 缺失任务时 Get 返回包裹 ErrJobNotFound 的错误，api 层据此映射 404
// （此前未包裹 → 被当成内部错误返回 500）。
func TestGetWrapsErrJobNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Get(context.Background(), "00000000-0000-0000-0000-0000000000ff", testTenant)
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Get(missing) err = %v want ErrJobNotFound", err)
	}
}

func TestListStepsReturnsStepsWithResultRef(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, err := s.Create(ctx, testTenant, testProject, string(KindNarration), "steps-1", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 先 render 后 tts：updated_at 递增，输出应保持时间序。
	for _, step := range []JobStep{
		{JobID: job.ID, TenantID: testTenant, StepType: "render", StepKey: "render:s1", State: StepSuccess, ResultRef: "ref-1"},
		{JobID: job.ID, TenantID: testTenant, StepType: "tts_segment", StepKey: "tts:s1", State: StepFailed},
	} {
		if err := s.MarkStep(ctx, step); err != nil {
			t.Fatalf("MarkStep(%s): %v", step.StepKey, err)
		}
		time.Sleep(5 * time.Millisecond) // 保证 updated_at 可区分，避免同刻排序不确定
	}
	steps, err := s.ListSteps(ctx, testTenant, job.ID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("steps = %d want 2", len(steps))
	}
	if steps[0].StepType != "render" || steps[1].StepType != "tts_segment" {
		t.Fatalf("order = [%s %s] want [render tts_segment]", steps[0].StepType, steps[1].StepType)
	}
	if steps[0].ResultRef != "ref-1" {
		t.Fatalf("result_ref = %q want ref-1", steps[0].ResultRef)
	}
	if steps[1].State != StepFailed {
		t.Fatalf("state = %s want failed", steps[1].State)
	}
	// 跨租户不可见（RLS + tenant_id 条件双重保证）。
	other, err := s.ListSteps(ctx, testOtherTenant, job.ID)
	if err != nil {
		t.Fatalf("cross-tenant ListSteps: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-tenant steps = %d want 0", len(other))
	}
}

func TestStepSummariesAggregatesAndDerivesPhaseFromLatestStep(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	jobA, err := s.Create(ctx, testTenant, testProject, string(KindNarration), "sum-1", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create jobA: %v", err)
	}
	jobB, err := s.Create(ctx, testTenant, testProject, string(KindParse), "sum-2", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create jobB: %v", err)
	}
	for _, step := range []JobStep{
		{JobID: jobA.ID, TenantID: testTenant, StepType: "tts_segment", StepKey: "tts:1", State: StepSuccess},
		{JobID: jobA.ID, TenantID: testTenant, StepType: "tts_segment", StepKey: "tts:2", State: StepSuccess},
		{JobID: jobA.ID, TenantID: testTenant, StepType: "alignment", StepKey: "align:1", State: StepFailed},
	} {
		if err := s.MarkStep(ctx, step); err != nil {
			t.Fatalf("MarkStep(%s): %v", step.StepKey, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	sums, err := s.StepSummaries(ctx, testTenant, []string{jobA.ID, jobB.ID})
	if err != nil {
		t.Fatalf("StepSummaries: %v", err)
	}
	a, ok := sums[jobA.ID]
	if !ok {
		t.Fatalf("jobA missing from summaries: %+v", sums)
	}
	if a.Total != 3 {
		t.Fatalf("jobA total = %d want 3", a.Total)
	}
	if a.Counts[StepSuccess] != 2 || a.Counts[StepFailed] != 1 {
		t.Fatalf("jobA counts = %+v", a.Counts)
	}
	// 阶段 = 最近更新的步骤类型（job_steps 无阶段列，只能由时间序推导）。
	if a.Phase != "alignment" {
		t.Fatalf("jobA phase = %q want alignment", a.Phase)
	}
	// 无步骤的任务不出现在聚合结果里（界面显示"—"，不伪造阶段）。
	if _, ok := sums[jobB.ID]; ok {
		t.Fatalf("jobB (no steps) should not appear: %+v", sums[jobB.ID])
	}
	empty, err := s.StepSummaries(ctx, testTenant, nil)
	if err != nil {
		t.Fatalf("StepSummaries(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty ids = %+v want empty", empty)
	}
}

func TestGetManyFiltersByTenantAndSkipsUnknown(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mine, err := s.Create(ctx, testTenant, testProject, string(KindParse), "many-1", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create mine: %v", err)
	}
	theirs, err := s.Create(ctx, testOtherTenant, testOtherProject, string(KindParse), "many-2", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create theirs: %v", err)
	}
	got, err := s.GetMany(ctx, testTenant, []string{
		mine.ID, theirs.ID, "00000000-0000-0000-0000-0000000000ff",
	})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("GetMany = %+v want only %s", got, mine.ID)
	}
	empty, err := s.GetMany(ctx, testTenant, nil)
	if err != nil {
		t.Fatalf("GetMany(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("GetMany(nil) = %+v want empty", empty)
	}
}
