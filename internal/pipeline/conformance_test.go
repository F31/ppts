package pipeline

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/tenant"
)

// 任务的契约测试：同一组行为断言跑 SQLite 与 PostgreSQL 两个实现。
//
// 与 internal/project 的那套同源（见那里的文件头注释）。任务侧比项目侧更要紧：
// 这里的状态机是**并发**的——领取要原子、租约要能过期、fencing 要能挡住被顶替的
// worker 提交结果。任何一条语义漂移都不会报错，只会表现成「偶尔重复执行一次」
// 或者「任务卡在 running 再也不动」。
//
// 另外还有一条从项目侧继承来的前提：run_at 的「零值」必须落成 NULL。
// run_at 的语义是「早于此刻不可领取」，零值 = 立刻可领；如果实现把零值写成
// 0001-01-01，调度仍然是对的（0001 < now），但所有「最老排队时间」的运维指标
// 都会指向公元 1 年。这条差异此前在 PG 上真实存在过，用它当回归用例。
type pipelineConformanceEnv struct {
	Store   conformanceStore
	Tenant  string
	Project string
}

// conformanceStore 是契约用例用到的全部方法。Store 端口之外那几个（积压指标、
// 幂等查询）只有具体实现才有，用局部接口把它们也纳入契约。
type conformanceStore interface {
	Store
	CountActive(ctx context.Context, tenantID string) (int, error)
	ByIdempotency(ctx context.Context, tenantID, kind, idemKey string) (*Job, error)
	OldestQueuedAge(ctx context.Context) (time.Duration, error)
	QueueDepthByKind(ctx context.Context, tenantID string) ([]QueueDepthStat, error)
}

func runPipelineStoreConformance(t *testing.T, newEnv func(t *testing.T) pipelineConformanceEnv) {
	cases := []struct {
		name string
		fn   func(*testing.T, context.Context, pipelineConformanceEnv)
	}{
		{"CreateIdempotency", conformanceJobCreateIdempotency},
		{"ImmediateRunAt", conformanceJobImmediateRunAt},
		{"DelayedRunAt", conformanceJobDelayedRunAt},
		{"ClaimLeaseFencing", conformanceJobClaimLeaseFencing},
		{"CompleteTerminal", conformanceJobCompleteTerminal},
		{"CancelSemantics", conformanceJobCancelSemantics},
		{"RetryFailed", conformanceJobRetryFailed},
		{"ListPaging", conformanceJobListPaging},
		{"EventsSince", conformanceJobEventsSince},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newEnv(t)
			// 租约类操作（Heartbeat/Complete/ClaimNext）在 PG 侧走 tenant.RunCtx，
			// 租户只在 context 里——这是 worker 的真实调用形态，用例照此模拟。
			ctx := tenant.WithContext(context.Background(), env.Tenant)
			c.fn(t, ctx, env)
		})
	}
}

func mustCreateJob(t *testing.T, env pipelineConformanceEnv, ctx context.Context, kind JobKind, idem string, runAt time.Time) *Job {
	t.Helper()
	j, err := env.Store.Create(ctx, env.Tenant, env.Project, string(kind), idem, `{"n":1}`, runAt)
	if err != nil {
		t.Fatalf("Create(%s,%s): %v", kind, idem, err)
	}
	return j
}

func conformanceJobCreateIdempotency(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	j := mustCreateJob(t, env, ctx, KindParse, "idem-1", time.Time{})
	if j.State != StateQueued || j.Attempt != 0 || j.FencingToken != 0 {
		t.Fatalf("新任务字段不对：%+v", j)
	}
	again := mustCreateJob(t, env, ctx, KindParse, "idem-1", time.Time{})
	if again.ID != j.ID {
		t.Fatalf("同键同种目应返回同一行：%s vs %s", again.ID, j.ID)
	}

	// 同幂等键 + 不同 kind 是两条任务（约束是三元组）。
	other := mustCreateJob(t, env, ctx, KindRender, "idem-1", time.Time{})
	if other.ID == j.ID {
		t.Fatal("不同 kind 复用了同一行")
	}

	got, err := env.Store.ByIdempotency(ctx, env.Tenant, string(KindParse), "idem-1")
	if err != nil || got.ID != j.ID {
		t.Fatalf("ByIdempotency: %+v err=%v", got, err)
	}
	if _, err := env.Store.ByIdempotency(ctx, env.Tenant, string(KindParse), "nope"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("ByIdempotency 未命中：%v, want ErrJobNotFound", err)
	}

	if n, err := env.Store.CountActive(ctx, env.Tenant); err != nil || n != 2 {
		t.Fatalf("CountActive = %d err=%v, want 2", n, err)
	}
}

// conformanceJobImmediateRunAt 守 run_at 零值语义。
//
// 零值表示「没有定时」，必须落成 NULL（或任何 <= now 的等价值）。PG 侧曾经把它
// 写成 0001-01-01：调度照常，但 QueueDepthByKind 的「最老可领取任务的 run_at」
// 变成公元 1 年，积压时长的看板从此不可用。
func conformanceJobImmediateRunAt(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	before := time.Now()
	j := mustCreateJob(t, env, ctx, KindParse, "immediate", time.Time{})
	if !j.RunAt.IsZero() && j.RunAt.Before(before.Add(-time.Minute)) {
		t.Fatalf("零值 run_at 落成了 %v（应保持零值/NULL）", j.RunAt)
	}

	stats, err := env.Store.QueueDepthByKind(ctx, env.Tenant)
	if err != nil {
		t.Fatalf("QueueDepthByKind: %v", err)
	}
	stat := findDepth(t, stats, string(KindParse))
	if stat.Waiting != 1 || stat.NotReady != 0 {
		t.Fatalf("即时任务的积压计数：%+v", stat)
	}
	if stat.OldestRunAt.IsZero() {
		t.Fatalf("即时任务应给出最老可领取时间：%+v", stat)
	}
	if stat.OldestRunAt.Before(before.Add(-time.Minute)) {
		t.Fatalf("最老可领取时间 = %v，早于创建时间（零值被当成真实时刻写进去了）", stat.OldestRunAt)
	}

	// 真的能领出来。
	claimed, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute)
	if err != nil || claimed.ID != j.ID {
		t.Fatalf("领取即时任务：%+v err=%v", claimed, err)
	}
}

func conformanceJobDelayedRunAt(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	j := mustCreateJob(t, env, ctx, KindRender, "delayed", at)
	if j.RunAt.IsZero() || j.RunAt.Sub(at).Abs() > time.Second {
		t.Fatalf("定时任务的 run_at = %v, want ~%v", j.RunAt, at)
	}

	if _, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute); !errors.Is(err, ErrNoJob) {
		t.Fatalf("未到点的任务被领走了：%v, want ErrNoJob", err)
	}
	stat := findDepth(t, mustDepth(t, ctx, env), string(KindRender))
	if stat.Waiting != 0 || stat.NotReady != 1 {
		t.Fatalf("定时任务应计入 notReady：%+v", stat)
	}
	if !stat.OldestRunAt.IsZero() {
		t.Fatalf("没有可领取任务时不应给出最老时间：%v", stat.OldestRunAt)
	}
}

func conformanceJobClaimLeaseFencing(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	j := mustCreateJob(t, env, ctx, KindParse, "lease", time.Time{})

	claimed, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("领取：%v", err)
	}
	if claimed.ID != j.ID || claimed.State != StateRunning || claimed.Attempt != 1 ||
		claimed.FencingToken != 1 || claimed.LeaseOwner != "worker-1" {
		t.Fatalf("领取结果不对：%+v", claimed)
	}
	if claimed.LeaseUntil.IsZero() || !claimed.LeaseUntil.After(time.Now()) {
		t.Fatalf("租约到期时间不对：%v", claimed.LeaseUntil)
	}

	// 租约未到期时同一个 worker 也领不到第二条（没有其它任务）。
	if _, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute); !errors.Is(err, ErrNoJob) {
		t.Fatalf("重复领取：%v, want ErrNoJob", err)
	}

	if err := env.Store.Heartbeat(ctx, claimed.ID, "worker-1", claimed.FencingToken, time.Minute); err != nil {
		t.Fatalf("续租：%v", err)
	}
	if err := env.Store.Heartbeat(ctx, claimed.ID, "worker-2", claimed.FencingToken, time.Minute); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("冒名续租：%v, want ErrLeaseMismatch", err)
	}
	if err := env.Store.Heartbeat(ctx, claimed.ID, "worker-1", claimed.FencingToken+1, time.Minute); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("过期 fencing 续租：%v, want ErrLeaseMismatch", err)
	}
}

func conformanceJobCompleteTerminal(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	j := mustCreateJob(t, env, ctx, KindParse, "complete", time.Time{})
	claimed, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("领取：%v", err)
	}

	if err := env.Store.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateSucceeded, nil); err != nil {
		t.Fatalf("提交成功：%v", err)
	}
	done, err := env.Store.Get(ctx, j.ID, env.Tenant)
	if err != nil || done.State != StateSucceeded || done.Progress != 100 || done.LeaseOwner != "" {
		t.Fatalf("终态行不对：%+v err=%v", done, err)
	}

	// 终态之后再提交必须被 fencing 挡住：这是「worker 被顶替后仍写完结果」的最后一道闸。
	if err := env.Store.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateFailed, nil); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("重复提交：%v, want ErrLeaseMismatch", err)
	}
	// 终态任务不能再被领走。
	if _, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute); !errors.Is(err, ErrNoJob) {
		t.Fatalf("领取已完成任务：%v, want ErrNoJob", err)
	}
	if n, err := env.Store.CountActive(ctx, env.Tenant); err != nil || n != 0 {
		t.Fatalf("终态仍计入活跃：n=%d err=%v", n, err)
	}
}

func conformanceJobCancelSemantics(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	// queued → 直接 canceled。
	queued := mustCreateJob(t, env, ctx, KindParse, "cancel-queued", time.Time{})
	got, err := env.Store.Cancel(ctx, queued.ID, env.Tenant)
	if err != nil || got.State != StateCanceled {
		t.Fatalf("取消排队任务：%+v err=%v", got, err)
	}
	if _, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute); !errors.Is(err, ErrNoJob) {
		t.Fatalf("已取消任务被领走：%v", err)
	}

	// running → cancel_requested，worker 下次续租时才知道。
	running := mustCreateJob(t, env, ctx, KindRender, "cancel-running", time.Time{})
	claimed, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("领取：%v", err)
	}
	if claimed.ID != running.ID {
		t.Fatalf("领到的不是 running 用例的任务：%s vs %s", claimed.ID, running.ID)
	}
	got, err = env.Store.Cancel(ctx, claimed.ID, env.Tenant)
	if err != nil || got.State != StateCancelReq {
		t.Fatalf("取消运行中任务：%+v err=%v", got, err)
	}
	if err := env.Store.Heartbeat(ctx, claimed.ID, "worker-1", claimed.FencingToken, time.Minute); !errors.Is(err, ErrCancelRequested) {
		t.Fatalf("取消请求应通过续租传递给 worker：%v, want ErrCancelRequested", err)
	}
	// worker 在安全点提交 canceled。
	if err := env.Store.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateCanceled, nil); err != nil {
		t.Fatalf("worker 提交取消：%v", err)
	}

	if _, err := env.Store.Cancel(ctx, uuid.NewString(), env.Tenant); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("取消不存在的任务：%v, want ErrJobNotFound", err)
	}
	if _, err := env.Store.Cancel(ctx, queued.ID, env.Tenant); !errors.Is(err, ErrJobNotCancelable) {
		t.Fatalf("取消已取消任务：%v, want ErrJobNotCancelable", err)
	}
}

func conformanceJobRetryFailed(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	j := mustCreateJob(t, env, ctx, KindNarration, "retry", time.Time{})
	claimed, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("领取：%v", err)
	}
	msg := []byte(`{"message":"boom"}`)
	if err := env.Store.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateFailed, msg); err != nil {
		t.Fatalf("置失败：%v", err)
	}

	retried, err := env.Store.RetryFailed(ctx, j.ID, env.Tenant)
	if err != nil {
		t.Fatalf("重试：%v", err)
	}
	if retried.State != StateQueued || retried.LastError != nil || !retried.RunAt.IsZero() {
		t.Fatalf("重试后的行不对：%+v", retried)
	}
	// 重新入队后可以被再领一次，fencing 继续递增（旧租约永久失效）。
	again, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-2", time.Minute)
	if err != nil || again.ID != j.ID {
		t.Fatalf("重试后领取：%+v err=%v", again, err)
	}
	if again.FencingToken <= claimed.FencingToken {
		t.Fatalf("fencing 没有递增：%d -> %d", claimed.FencingToken, again.FencingToken)
	}
	if err := env.Store.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateSucceeded, nil); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("旧租约仍能提交：%v, want ErrLeaseMismatch", err)
	}
	if _, err := env.Store.RetryFailed(ctx, j.ID, env.Tenant); !errors.Is(err, ErrJobNotRetryable) {
		t.Fatalf("重试非失败任务：%v, want ErrJobNotRetryable", err)
	}
}

// conformanceJobListPaging 与项目侧同理：同一毫秒创建的多个任务会一起越过
// 只按时间的游标边界，上一页截断、下一页跳过——批量导出一次几十个任务时最明显。
func conformanceJobListPaging(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	const n = 5
	want := make([]string, 0, n)
	for i := 0; i < n; i++ {
		want = append(want, mustCreateJob(t, env, ctx, KindExport, "page-"+uuid.NewString(), time.Time{}).ID)
	}

	var got []*Job
	cursor := ""
	for i := 0; i < 20; i++ {
		items, next, err := env.Store.List(ctx, env.Tenant, env.Project, "", cursor, 2)
		if err != nil {
			t.Fatalf("第 %d 页：%v", i+1, err)
		}
		if len(items) > 2 {
			t.Fatalf("页大小失效：%d", len(items))
		}
		got = append(got, items...)
		if next == "" {
			break
		}
		if next == cursor {
			t.Fatalf("游标没有推进：%q", next)
		}
		cursor = next
	}
	seen := map[string]int{}
	gotIDs := make([]string, 0, len(got))
	for _, j := range got {
		seen[j.ID]++
		gotIDs = append(gotIDs, j.ID)
	}
	for id, c := range seen {
		if c > 1 {
			t.Fatalf("任务 %s 在翻页中重复出现 %d 次", id, c)
		}
	}
	sort.Strings(gotIDs)
	sort.Strings(want)
	if len(gotIDs) != len(want) {
		t.Fatalf("翻页拿到 %d 条，期望 %d 条（丢行）", len(gotIDs), len(want))
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("翻页结果与期望集合不一致\n got=%v\nwant=%v", gotIDs, want)
		}
	}

	if _, _, err := env.Store.List(ctx, env.Tenant, env.Project, "", "not-a-cursor", 2); !errors.Is(err, ErrBadJobCursor) {
		t.Fatalf("非法游标：%v, want ErrBadJobCursor", err)
	}
}

func conformanceJobEventsSince(t *testing.T, ctx context.Context, env pipelineConformanceEnv) {
	j := mustCreateJob(t, env, ctx, KindParse, "events", time.Time{})
	claimed, err := env.Store.ClaimNext(ctx, env.Tenant, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("领取：%v", err)
	}
	if err := env.Store.Complete(ctx, claimed.ID, "worker-1", claimed.FencingToken, StateSucceeded, nil); err != nil {
		t.Fatalf("提交：%v", err)
	}

	all, err := env.Store.EventsSince(ctx, env.Tenant, env.Project, 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(all) < 2 {
		// 两侧都只在「状态/进度真正落库」时记事件（Create + Complete），领取本身不记。
		t.Fatalf("事件数 = %d，至少应有入队与终态两条", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("事件序号不是递增：%d -> %d", all[i-1].Seq, all[i].Seq)
		}
	}
	last := all[len(all)-1]
	if last.Job == nil || last.Job.ID != j.ID || last.Job.State != StateSucceeded {
		t.Fatalf("最新事件不对：%+v", last.Job)
	}

	// afterSeq 是断点续传的唯一依据：必须严格大于。
	tail, err := env.Store.EventsSince(ctx, env.Tenant, env.Project, all[0].Seq, 100)
	if err != nil {
		t.Fatalf("断点续传：%v", err)
	}
	if len(tail) != len(all)-1 || tail[0].Seq != all[1].Seq {
		t.Fatalf("afterSeq 过滤不对：n=%d first=%d", len(tail), tail[0].Seq)
	}
	limited, err := env.Store.EventsSince(ctx, env.Tenant, env.Project, 0, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit 失效：n=%d err=%v", len(limited), err)
	}
}

func findDepth(t *testing.T, stats []QueueDepthStat, kind string) QueueDepthStat {
	t.Helper()
	for _, s := range stats {
		if s.Kind == kind {
			return s
		}
	}
	t.Fatalf("积压统计里没有 %s：%+v", kind, stats)
	return QueueDepthStat{}
}

func mustDepth(t *testing.T, ctx context.Context, env pipelineConformanceEnv) []QueueDepthStat {
	t.Helper()
	stats, err := env.Store.QueueDepthByKind(ctx, env.Tenant)
	if err != nil {
		t.Fatalf("QueueDepthByKind: %v", err)
	}
	return stats
}

// TestSQLitePipelineConformance 用本地库跑同一套用例。
func TestSQLitePipelineConformance(t *testing.T) {
	runPipelineStoreConformance(t, func(t *testing.T) pipelineConformanceEnv {
		store := newPipelineStore(t)
		projectID := uuid.NewString()
		ctx := context.Background()
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO projects (id, tenant_id, owner_user, title) VALUES (?, ?, ?, ?)`,
			projectID, db.LocalTenantID, db.LocalUserID, "conformance"); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		return pipelineConformanceEnv{Store: store, Tenant: db.LocalTenantID, Project: projectID}
	})
}
