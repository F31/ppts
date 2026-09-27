package observability

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"testing"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

type queueDepthFake struct {
	stats []pipeline.QueueDepthStat
	err   error
	calls int
}

func (f *queueDepthFake) QueueDepthByKind(context.Context, string) ([]pipeline.QueueDepthStat, error) {
	f.calls++
	return f.stats, f.err
}

// TestQueueDepthReporterWritesPerKindGauges 要求每个 kind 各自成键。
//
// 这条用例锁的失效模式是"把各 kind 合并成一个总数" —— 那正是本轮要消除的局面：
// 队列长度 7 看不出是谁在堵，拆开之后哪个 kind 在饿死别人一目了然。
func TestQueueDepthReporterWritesPerKindGauges(t *testing.T) {
	fake := &queueDepthFake{stats: []pipeline.QueueDepthStat{
		{Kind: "narration", Waiting: 4, Running: 2, OldestRunAt: time.Now().Add(-90 * time.Second)},
		{Kind: "export", Waiting: 0, Running: 1, NotReady: 3},
	}}
	r := NewQueueDepthReporter(fake, "t1", time.Minute, nil)
	r.report(context.Background())

	if got := intOf(t, queueDepthByKind, "kind=narration"); got != 4 {
		t.Errorf("narration depth = %d, want 4", got)
	}
	if got := intOf(t, queueRunningByKind, "kind=narration"); got != 2 {
		t.Errorf("narration running = %d, want 2", got)
	}
	if got := intOf(t, queueBackoffByKind, "kind=export"); got != 3 {
		t.Errorf("export backoff = %d, want 3", got)
	}
	// export 没有排队任务：等待时长不该被写成 0（未定义 ≠ 零）。
	if _, err := mapValue(queueOldestWaitByKind, "kind=export"); err == nil {
		t.Error("export has no waiting job: oldest wait must stay unset, writing 0 would read as 'no backlog'")
	}
	// narration 有排队任务：等待时长应接近 90 秒。
	wait := floatOf(t, queueOldestWaitByKind, "kind=narration")
	if wait < 85 || wait > 100 {
		t.Errorf("narration oldest wait = %.1fs, want ~90s", wait)
	}
}

// TestQueueDepthReporterSurvivesReadFailure 要求读失败不打挂 worker，也不把旧值清零。
//
// 采样失败时把旧值清零是错的：那会让"采集挂了"伪装成"队列空了"，
// 而后者的误导方向正对着运维（不会有人去查一个看起来健康的队列）。
func TestQueueDepthReporterSurvivesReadFailure(t *testing.T) {
	fake := &queueDepthFake{stats: []pipeline.QueueDepthStat{{Kind: "parse", Waiting: 7, Running: 1}}}
	r := NewQueueDepthReporter(fake, "t1", time.Minute, nil)
	r.report(context.Background())
	if got := intOf(t, queueDepthByKind, "kind=parse"); got != 7 {
		t.Fatalf("setup: parse depth = %d, want 7", got)
	}

	fake.err = errors.New("db down")
	r.report(context.Background())
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2", fake.calls)
	}
	if got := intOf(t, queueDepthByKind, "kind=parse"); got != 7 {
		t.Errorf("after read failure depth = %d, want stale 7（清 0 会把采集故障伪装成队列已空）", got)
	}
}

// mapValue 取 map 里的一个键。**键不存在必须是错误而不是零值** ——
// "没见过这个 kind"与"这个 kind 当前积压为 0"是两种状态，在测试里混起来会漏掉漏埋指标的情况。
func mapValue(m *expvar.Map, key string) (expvar.Var, error) {
	var (
		found expvar.Var
		ok    bool
	)
	m.Do(func(kv expvar.KeyValue) {
		if kv.Key == key {
			found, ok = kv.Value, true
		}
	})
	if !ok {
		return nil, fmt.Errorf("key %q absent", key)
	}
	return found, nil
}

func intOf(t *testing.T, m *expvar.Map, key string) int64 {
	t.Helper()
	v, err := mapValue(m, key)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	i, ok := v.(*expvar.Int)
	if !ok {
		t.Fatalf("value at %q is %T, want expvar.Int", key, v)
	}
	return i.Value()
}

func floatOf(t *testing.T, m *expvar.Map, key string) float64 {
	t.Helper()
	v, err := mapValue(m, key)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	f, ok := v.(*expvar.Float)
	if !ok {
		t.Fatalf("value at %q is %T, want expvar.Float", key, v)
	}
	return f.Value()
}
