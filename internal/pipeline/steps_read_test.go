//go:build pg

// B4-M6a/M6b 的 PG 端到端测试：任务步骤读取、批量任务查询，
// 以及 M6b 新增的 phase/affected_pages 写入与 ListPage 筛选/排序/游标。
// 与 postgres_test.go 同构建标签与 helper（testStore/testTenant 等）。
// 需要 PPTS_TEST_DATABASE；未设置时 testStore 直接 t.Skip（仓库惯例：环境依赖型测试跳过而非失败）。
package pipeline

import (
	"context"
	"errors"
	"strconv"
	"strings"
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

// B4-M6b：MarkStep 必须在**同一事务**内维护 jobs.phase（阶段 = 最近写入的步骤类型），
// 这样阶段才不会与步骤漂移，敢于被列表排序/筛选直接使用。
func TestMarkStepMaintainsJobsPhase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job, err := s.Create(ctx, testTenant, testProject, string(KindNarration), "phase-1", "snap", time.Time{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 尚无步骤：阶段为空串（界面显示"—"，不伪造）。
	if phase := phaseOf(t, s, ctx, job.ID); phase != "" {
		t.Fatalf("initial phase = %q want empty", phase)
	}
	for _, stepType := range []string{"pages", "tts_segment", "timeline", "export"} {
		if err := s.MarkStep(ctx, JobStep{
			JobID: job.ID, TenantID: testTenant,
			StepType: stepType, StepKey: stepType + ":1", State: StepSuccess,
		}); err != nil {
			t.Fatalf("MarkStep(%s): %v", stepType, err)
		}
		if phase := phaseOf(t, s, ctx, job.ID); phase != stepType {
			t.Fatalf("phase after %s = %q want %q", stepType, phase, stepType)
		}
	}
	// 重复写同一步骤不应改变阶段；跨租户不可见。
	if err := s.MarkStep(ctx, JobStep{
		JobID: job.ID, TenantID: testTenant,
		StepType: "export", StepKey: "export:1", State: StepSuccess,
	}); err != nil {
		t.Fatalf("MarkStep(repeat): %v", err)
	}
	if phase := phaseOf(t, s, ctx, job.ID); phase != "export" {
		t.Fatalf("phase after repeat = %q want export", phase)
	}
	rows, _, err := s.ListPage(ctx, testOtherTenant, JobFilter{}, "", 10)
	if err != nil {
		t.Fatalf("cross-tenant ListPage: %v", err)
	}
	for _, r := range rows {
		if r.Job.ID == job.ID {
			t.Fatalf("cross-tenant ListPage leaked job %s", job.ID)
		}
	}
}

// B4-M6b：Create 由 input_snapshot 提取 affected_pages 落库，使「按受影响页数排序」可在 SQL 内完成。
func TestCreateWritesAffectedPages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	cases := []struct {
		name     string
		kind     JobKind
		snapshot string
		want     int
	}{
		{"narration 三页", KindNarration, `{"slides":[{"slideId":"s1"},{"slideId":"s2"},{"slideId":"s3"}],"segmentIds":[]}`, 3},
		{"narration 去重", KindNarration, `{"slides":[{"slideId":"s1"},{"slideId":"s1"}],"segmentIds":[]}`, 1},
		{"script_draft 指定页", KindScriptDraft, `{"slideIds":["a","b"],"revisionNo":1}`, 2},
		{"parse 无页概念", KindParse, `{"revisionNo":1}`, 0},
		{"空快照", KindNarration, ``, 0},
	}
	for _, tc := range cases {
		job, err := s.Create(ctx, testTenant, testProject, string(tc.kind), "pages-"+tc.name, tc.snapshot, time.Time{})
		if err != nil {
			t.Fatalf("Create(%s): %v", tc.name, err)
		}
		if got := pageCountOf(t, s, ctx, job.ID); got != tc.want {
			t.Fatalf("%s: affected_pages len = %d want %d", tc.name, got, tc.want)
		}
	}
}

// B4-M6b：列表的筛选/排序/游标翻页端到端行为。
func TestListPageFiltersSortsAndPaginates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 三个任务：页数 1/2/3，阶段分别为 tts_segment / export / pages。
	specs := []struct {
		idem     string
		stepType string
		slides   int
	}{
		{"lp-1", "tts_segment", 1},
		{"lp-2", "export", 2},
		{"lp-3", "pages", 3},
	}
	ids := make([]string, 0, len(specs))
	for _, sp := range specs {
		job, err := s.Create(ctx, testTenant, testProject, string(KindNarration), sp.idem,
			narrationSnapshot(sp.slides), time.Time{})
		if err != nil {
			t.Fatalf("Create(%s): %v", sp.idem, err)
		}
		if err := s.MarkStep(ctx, JobStep{
			JobID: job.ID, TenantID: testTenant,
			StepType: sp.stepType, StepKey: sp.stepType + ":1", State: StepSuccess,
		}); err != nil {
			t.Fatalf("MarkStep(%s): %v", sp.stepType, err)
		}
		ids = append(ids, job.ID)
	}

	// 1) 阶段筛选：只回匹配项，且数量正确。
	rows, _, err := s.ListPage(ctx, testTenant, JobFilter{Phase: "export"}, "", 50)
	if err != nil {
		t.Fatalf("ListPage(phase=export): %v", err)
	}
	found := 0
	for _, r := range rows {
		if r.Job.ID == ids[1] {
			found++
		}
		if r.Phase != "export" {
			t.Fatalf("phase filter leaked row with phase %q", r.Phase)
		}
	}
	if found != 1 {
		t.Fatalf("phase=export matched %d of our jobs want 1", found)
	}

	// 2) 按受影响页数降序：我们的三个任务应严格递减 3,2,1。
	rows, _, err = s.ListPage(ctx, testTenant, JobFilter{Sort: "pages", Desc: true}, "", 100)
	if err != nil {
		t.Fatalf("ListPage(sort=pages): %v", err)
	}
	var gotPages []int
	for _, r := range rows {
		if r.Job.ID == ids[0] || r.Job.ID == ids[1] || r.Job.ID == ids[2] {
			gotPages = append(gotPages, r.PageCount)
		}
	}
	if len(gotPages) != 3 || gotPages[0] != 3 || gotPages[1] != 2 || gotPages[2] != 1 {
		t.Fatalf("pages desc = %v want [3 2 1]", gotPages)
	}

	// 3) keyset 翻页：逐页取完，不得重复也不得漏项。
	seen := map[string]int{}
	cursor := ""
	for i := 0; i < 10; i++ {
		page, next, err := s.ListPage(ctx, testTenant, JobFilter{}, cursor, 2)
		if err != nil {
			t.Fatalf("ListPage(page %d): %v", i, err)
		}
		for _, r := range page {
			seen[r.Job.ID]++
		}
		if next == "" {
			break
		}
		cursor = next
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("job %s seen %d times across pages want 1", id, seen[id])
		}
	}

	// 4) 非法游标：返回 ErrBadJobCursor（api 映射为 400 而不是 500）。
	if _, _, err := s.ListPage(ctx, testTenant, JobFilter{}, "!!!", 2); !errors.Is(err, ErrBadJobCursor) {
		t.Fatalf("bad cursor err = %v want ErrBadJobCursor", err)
	}
}

// PhaseCounts 按阶段分组计数（可按项目过滤）。
func TestPhaseCountsGroupsByPhase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	jobA, err := s.Create(ctx, testTenant, testProject, string(KindNarration), "pc-1", narrationSnapshot(1), time.Time{})
	if err != nil {
		t.Fatalf("Create jobA: %v", err)
	}
	jobB, err := s.Create(ctx, testTenant, testProject, string(KindNarration), "pc-2", narrationSnapshot(1), time.Time{})
	if err != nil {
		t.Fatalf("Create jobB: %v", err)
	}
	for _, job := range []*Job{jobA, jobB} {
		if err := s.MarkStep(ctx, JobStep{
			JobID: job.ID, TenantID: testTenant, StepType: "export",
			StepKey: "export:1", State: StepSuccess,
		}); err != nil {
			t.Fatalf("MarkStep: %v", err)
		}
	}
	counts, err := s.PhaseCounts(ctx, testTenant, testProject)
	if err != nil {
		t.Fatalf("PhaseCounts: %v", err)
	}
	if counts["export"] < 2 {
		t.Fatalf("phase counts = %+v want at least 2 for export", counts)
	}
}

// narrationSnapshot 构造含 n 页的 narration 输入快照。
func narrationSnapshot(n int) string {
	var b strings.Builder
	b.WriteString(`{"slides":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"slideId":"s`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteString(`],"segmentIds":[]}`)
	return b.String()
}

// phaseOf 通过 ListPage 读回某任务的阶段（避免测试直接写 SQL 触碰 RLS 上下文）。
func phaseOf(t *testing.T, s *PGStore, ctx context.Context, jobID string) string {
	t.Helper()
	rows, _, err := s.ListPage(ctx, testTenant, JobFilter{}, "", 100)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	for _, r := range rows {
		if r.Job.ID == jobID {
			return r.Phase
		}
	}
	return ""
}

// pageCountOf 通过 ListPage 读回某任务的受影响页数。
func pageCountOf(t *testing.T, s *PGStore, ctx context.Context, jobID string) int {
	t.Helper()
	rows, _, err := s.ListPage(ctx, testTenant, JobFilter{}, "", 100)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	for _, r := range rows {
		if r.Job.ID == jobID {
			return r.PageCount
		}
	}
	return -1
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
