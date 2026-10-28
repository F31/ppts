package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newServiceWith 允许用例覆盖配置；WorkDir 始终是临时目录。
func newServiceWith(t *testing.T, mut func(*ServiceConfig)) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := ServiceConfig{
		Backend: &countingBackend{},
		WorkDir: dir,
		Version: "test-1",
	}
	mut(&cfg)
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc, dir
}

// pagesPlan 造一份 n 页、hash 互不相同的计划（SlideID 参与 planHash）。
func pagesPlan(tag string, pages int) *Timeline {
	plan := validPlan()
	first := plan.Pages[0]
	plan.Pages = plan.Pages[:0]
	for i := 0; i < pages; i++ {
		p := first
		p.Index = i
		p.SlideID = fmt.Sprintf("%s-%d", tag, i)
		plan.Pages = append(plan.Pages, p)
	}
	return plan
}

// waitUntilState 等到任务进入指定状态。
func waitUntilState(t *testing.T, svc *Service, id string, want JobState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := svc.Status(id)
		if !ok {
			t.Fatalf("job %s 不存在", id)
		}
		if st.State == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	st, _ := svc.Status(id)
	t.Fatalf("job %s 未在 5s 内进入 %s（当前 %s）", id, want, st.State)
}

// TestQueueFullRejectsNewSubmits 守住容量上限：排队满了就不再受理，
// 且被拒的提交不留任何记录（否则拒绝本身也在消耗内存）。
func TestQueueFullRejectsNewSubmits(t *testing.T) {
	be := &countingBackend{delay: 40 * time.Millisecond}
	svc, _ := newServiceWith(t, func(c *ServiceConfig) {
		c.Backend = be
		c.MaxConcurrent = 1
		c.MaxQueued = 1
	})

	idA, err := svc.Submit(pagesPlan("a", 3))
	if err != nil {
		t.Fatal(err)
	}
	waitUntilState(t, svc, idA, StateRunning)

	idB, err := svc.Submit(pagesPlan("b", 1))
	if err != nil {
		t.Fatalf("第二个任务应进队列: %v", err)
	}

	if _, err := svc.Submit(pagesPlan("c", 1)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("队列满时提交 err = %v, want ErrQueueFull", err)
	}

	svc.mu.Lock()
	jobs, attempts := len(svc.jobs), len(svc.attempts)
	svc.mu.Unlock()
	if jobs != 2 {
		t.Errorf("被拒的提交留下了 job 记录：jobs = %d, want 2", jobs)
	}
	// 被拒的提交也不该消耗重试编号，否则 id 会凭空跳号。
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}

	waitState(t, svc, idA)
	waitState(t, svc, idB)

	// 队列消化后必须重新可以提交，否则一次打满就等于永久拒服。
	// 提交后要等它落终态：TempDir 清理撞上仍在写的 worker 是 Windows 上的经典假红灯。
	last, err := svc.Submit(pagesPlan("c", 1))
	if err != nil {
		t.Fatalf("队列消化后仍拒绝提交: %v", err)
	}
	waitState(t, svc, last)
}

// TestHTTPQueueFullReturns429 固定 HTTP 语义：429 + Retry-After + 可识别的错误码。
// 客户端契约里 429 本就属于「可退避重试」，用 503 会把容量问题说成服务故障。
func TestHTTPQueueFullReturns429(t *testing.T) {
	be := &countingBackend{delay: 40 * time.Millisecond}
	svc, _ := newServiceWith(t, func(c *ServiceConfig) {
		c.Backend = be
		c.MaxConcurrent = 1
		c.MaxQueued = 1
	})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	submit := func(plan *Timeline) (*http.Response, []byte) {
		t.Helper()
		body, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(srv.URL+"/v1/render", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, raw
	}
	idOf := func(raw []byte) string {
		var st JobStatus
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("响应不可解析: %v (%s)", err, raw)
		}
		return st.ID
	}

	resp, raw := submit(pagesPlan("a", 3))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("首个任务 status = %d", resp.StatusCode)
	}
	waitUntilState(t, svc, idOf(raw), StateRunning)

	if resp, _ := submit(pagesPlan("b", 1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("排队任务 status = %d", resp.StatusCode)
	}

	resp, body := submit(pagesPlan("c", 1))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("队列满 status = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Error("429 缺少 Retry-After，客户端只能瞎猜退避时长")
	}
	var e struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("错误体不可解析: %v", err)
	}
	if e.Code != "queue_full" {
		t.Errorf("code = %q, want queue_full", e.Code)
	}
}

// TestExpiredJobsAreReclaimed 守住保留策略：终态任务在 TTL 内可查，
// 过期后记录、planHash 映射与产物目录一起消失（否则长跑必然吃满磁盘）。
func TestExpiredJobsAreReclaimed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, dir := newServiceWith(t, func(c *ServiceConfig) {
		c.Now = func() time.Time { return now }
		c.JobTTL = time.Minute
		// 关掉惰性节流，改用 sweepExpired 精确控制时间点。
		c.RetentionSweep = time.Hour
	})
	be := svc.cfg.Backend.(*countingBackend)

	id, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, svc, id); st.State != StateSucceeded {
		t.Fatalf("state = %s", st.State)
	}
	jobDir := filepath.Join(dir, id)
	if _, err := os.Stat(filepath.Join(jobDir, "segment_000.mp4.stub")); err != nil {
		t.Fatalf("产物未落盘: %v", err)
	}

	// TTL 内：任务与产物都必须还在，客户端还来得及取。
	if got := svc.sweepExpired(now.Add(30 * time.Second)); len(got) != 0 {
		t.Fatalf("TTL 内被回收: %v", got)
	}
	if _, ok := svc.Status(id); !ok {
		t.Fatal("TTL 内 job 就查不到了")
	}

	// 过期：记录、目录一并清掉。
	if got := svc.sweepExpired(now.Add(90 * time.Second)); len(got) != 1 || got[0] != id {
		t.Fatalf("回收结果 = %v, want [%s]", got, id)
	}
	if _, ok := svc.Status(id); ok {
		t.Error("过期后 job 仍可查询")
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("过期后产物目录仍在: %v", err)
	}
	svc.mu.Lock()
	_, hasHash := svc.byHash[validHash(t, svc)]
	attempts := len(svc.attempts)
	svc.mu.Unlock()
	if hasHash {
		t.Error("过期后 planHash 仍映射到已删除的 job")
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0（它不能成为第三张只增不减的表）", attempts)
	}

	// 同一份计划再提交必须真的重渲（此时旧产物已被清掉，复用记录会返回空产物）。
	id2, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, svc, id2); st.State != StateSucceeded || len(st.Artifacts) == 0 {
		t.Fatalf("过期后重提状态 = %s, artifacts = %v", st.State, st.Artifacts)
	}
	if got := be.Calls(); got != 2 {
		t.Fatalf("backend 调用次数 = %d, want 2", got)
	}
}

// TestReapKeepsLiveJobs 防过度回收：running/queued 与未过期的终态都不该被动。
func TestReapKeepsLiveJobs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	be := &countingBackend{delay: 40 * time.Millisecond}
	svc, _ := newServiceWith(t, func(c *ServiceConfig) {
		c.Backend = be
		c.Now = func() time.Time { return now }
		c.JobTTL = time.Minute
		c.MaxConcurrent = 1
		c.MaxQueued = 4
	})

	running, err := svc.Submit(pagesPlan("run", 3))
	if err != nil {
		t.Fatal(err)
	}
	waitUntilState(t, svc, running, StateRunning)
	queued, err := svc.Submit(pagesPlan("wait", 1))
	if err != nil {
		t.Fatal(err)
	}

	// 即便把时钟推到很远的将来，非终态任务也不能被回收：它们没有 finishedAt，
	// 正在写目录或还没开始写。
	if got := svc.sweepExpired(now.Add(24 * time.Hour)); len(got) != 0 {
		t.Fatalf("非终态任务被回收: %v", got)
	}
	for _, id := range []string{running, queued} {
		if _, ok := svc.Status(id); !ok {
			t.Errorf("job %s 被误删", id)
		}
	}
	waitState(t, svc, running)
	waitState(t, svc, queued)
}

// TestCancelQueuedReleasesPlan 守住内存边界：排队中被取消的任务必须立刻放掉计划引用，
// 而不是攥着 4MB 一直排到信号量轮到它。
func TestCancelQueuedReleasesPlan(t *testing.T) {
	be := &countingBackend{delay: 50 * time.Millisecond}
	svc, _ := newServiceWith(t, func(c *ServiceConfig) {
		c.Backend = be
		c.MaxConcurrent = 1
		c.MaxQueued = 4
	})

	head, err := svc.Submit(pagesPlan("head", 3))
	if err != nil {
		t.Fatal(err)
	}
	waitUntilState(t, svc, head, StateRunning)

	queued, err := svc.Submit(pagesPlan("tail", 1))
	if err != nil {
		t.Fatal(err)
	}
	waitUntilState(t, svc, queued, StateQueued)

	if !svc.Cancel(queued) {
		t.Fatal("取消应返回 true")
	}

	svc.mu.Lock()
	j := svc.jobs[queued]
	plan, state := j.plan, j.state
	svc.mu.Unlock()
	if state != StateCanceled {
		t.Fatalf("state = %s, want canceled", state)
	}
	if plan != nil {
		t.Error("排队中被取消的任务仍持有 plan：取消风暴会把它变成无上限的内存占用")
	}
	if _, ok := svc.Status(queued); !ok {
		t.Error("取消后的 job 必须仍可查询到终态")
	}
	waitState(t, svc, head)
}

// TestHealthReportsQueueSeparately 固定观测口径：running 只能是真正在跑的，
// 排队中的必须单独报，否则「几个渲染进程在吃内存」这个最该看数的指标是假的。
func TestHealthReportsQueueSeparately(t *testing.T) {
	be := &countingBackend{delay: 40 * time.Millisecond}
	svc, _ := newServiceWith(t, func(c *ServiceConfig) {
		c.Backend = be
		c.MaxConcurrent = 1
		c.MaxQueued = 3
	})

	running, err := svc.Submit(pagesPlan("h1", 3))
	if err != nil {
		t.Fatal(err)
	}
	waitUntilState(t, svc, running, StateRunning)
	queued, err := svc.Submit(pagesPlan("h2", 1))
	if err != nil {
		t.Fatal(err)
	}
	waitUntilState(t, svc, queued, StateQueued)

	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if h.Running != 1 {
		t.Errorf("running = %d, want 1（queued 不该混进来）", h.Running)
	}
	if h.Queued != 1 || h.MaxQueued != 3 {
		t.Errorf("queued = %d, maxQueued = %d, want 1/3", h.Queued, h.MaxQueued)
	}
	if h.Retained != 0 {
		t.Errorf("retained = %d, want 0", h.Retained)
	}
	waitState(t, svc, running)
	waitState(t, svc, queued)
}

// validHash 算出 validPlan 在当前服务配置下的 planHash。
func validHash(t *testing.T, svc *Service) string {
	t.Helper()
	h, err := validPlan().Hash(HashOptions{RendererVersion: svc.cfg.Version})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
