package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestRetryAfterFailureRendersAgain 守住「失败不该毒化 planHash」。
//
// 真实渲染会因 Chrome 崩溃、超时、OOM 失败，用户最常见的动作是重试同一份计划。
// 若 byHash 在失败后仍指向旧 job，重试会直接拿到 failed 状态而永远不再渲染。
func TestRetryAfterFailureRendersAgain(t *testing.T) {
	be := &countingBackend{fail: fmt.Errorf("chrome crashed")}
	svc, dir := newTestService(t, be)

	id1, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, svc, id1); st.State != StateFailed {
		t.Fatalf("首次状态 = %s, want failed", st.State)
	}

	be.SetFail(nil)

	id2, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatalf("重试应分配新的 jobId（目录不能与上一轮重叠），却仍是 %s", id1)
	}
	st := waitState(t, svc, id2)
	if st.State != StateSucceeded {
		t.Fatalf("重试后状态 = %s (%s), want succeeded", st.State, st.Error)
	}
	if got := be.Calls(); got != 2 {
		t.Fatalf("backend 调用次数 = %d, want 2（重试必须真的重渲）", got)
	}
	// 重试的产物必须真的在盘上：若旧 job 的清理晚于新 job 的写入，这里会看到目录被删空。
	if _, err := os.Stat(filepath.Join(dir, id2, "segment_000.mp4.stub")); err != nil {
		t.Fatalf("重试产物缺失: %v", err)
	}
	// 旧 jobId 仍可查询且保持自己的终态：重试是新的一次尝试，不该改写历史。
	if st, ok := svc.Status(id1); !ok || st.State != StateFailed {
		t.Fatalf("旧 job 状态 = %s (ok=%v), want failed", st.State, ok)
	}
}

// TestRetryAfterCancelRendersAgain 取消是「这次不要了」，不是「这份计划永远不能渲染」。
func TestRetryAfterCancelRendersAgain(t *testing.T) {
	be := &countingBackend{delay: 20 * time.Millisecond}
	svc, _ := newTestService(t, be)

	id1, err := svc.Submit(slowPlan(20))
	if err != nil {
		t.Fatal(err)
	}
	// 必须等它真的起跑再取消：排队期取消不会调用 backend，
	// 那样「重试确实重渲了」就不是这个用例能证明的事。
	waitRunning(t, svc, id1)
	if !svc.Cancel(id1) {
		t.Fatal("取消应返回 true")
	}
	if st := waitState(t, svc, id1); st.State != StateCanceled {
		t.Fatalf("取消后状态 = %s, want canceled", st.State)
	}

	// 重试会拿到新的 jobId：上一轮可能还在清理自己的目录，两轮不能共用路径。
	id2, err := svc.Submit(slowPlan(20))
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatalf("取消后重试应分配新的 jobId，却仍是 %s", id1)
	}
	if st := waitState(t, svc, id2); st.State != StateSucceeded {
		t.Fatalf("取消后重试状态 = %s (%s), want succeeded", st.State, st.Error)
	}
	if got := be.Calls(); got != 2 {
		t.Fatalf("backend 调用次数 = %d, want 2", got)
	}
}

// TestDeleteReportsActualState DELETE 的响应必须说真话：
// 对已完成的任务点取消，它并没有被取消，不能回一个 canceled。
func TestDeleteReportsActualState(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body, _ := json.Marshal(validPlan())
	resp, err := http.Post(srv.URL+"/v1/render", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created JobStatus
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if st := waitState(t, svc, created.ID); st.State != StateSucceeded {
		t.Fatalf("前置状态 = %s", st.State)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/render/"+created.ID, nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer del.Body.Close()
	var got JobStatus
	if err := json.NewDecoder(del.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded {
		t.Fatalf("DELETE 响应状态 = %s, want %s（已完成的任务不会被取消）", got.State, StateSucceeded)
	}
}

// TestConcurrentSubmitCancelConsistency 是本机可跑的「粗粒度竞态」检查：
// 本机无 cgo 跑不了 -race，这个用例至少覆盖丢任务、死锁、终态与磁盘不一致、信号量泄漏。
func TestConcurrentSubmitCancelConsistency(t *testing.T) {
	const (
		n             = 24
		maxConcurrent = 4
	)
	dir := t.TempDir()
	be := &countingBackend{delay: 5 * time.Millisecond}
	svc, err := NewService(ServiceConfig{
		Backend: be, WorkDir: dir, Version: "test-1", MaxConcurrent: maxConcurrent,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 阶段一：并发提交不同计划。ids 只在各自 goroutine 里写，join 后才读，
	// 避免测试自己制造竞态。
	ids := make([]string, n)
	var submitWG sync.WaitGroup
	for i := 0; i < n; i++ {
		submitWG.Add(1)
		go func(i int) {
			defer submitWG.Done()
			id, err := svc.Submit(stressPlan(i))
			if err != nil {
				t.Errorf("submit %d: %v", i, err)
				return
			}
			ids[i] = id
		}(i)
	}
	submitWG.Wait()

	// 阶段二：并发取消其中一半。此时 24 个任务里最多 4 个在跑，其余仍在排队，
	// 两种取消路径都能覆盖到。
	var cancelWG sync.WaitGroup
	for i := 0; i < n; i += 2 {
		cancelWG.Add(1)
		go func(i int) {
			defer cancelWG.Done()
			time.Sleep(time.Duration(i%3) * time.Millisecond)
			if !svc.Cancel(ids[i]) {
				t.Errorf("cancel %d 返回 false", i)
			}
		}(i)
	}
	cancelWG.Wait()

	// 阶段三：并发重复提交同一计划，必须收敛到同一个 job。
	dedupeIDs := make([]string, 8)
	var dedupe sync.WaitGroup
	for i := range dedupeIDs {
		dedupe.Add(1)
		go func(i int) {
			defer dedupe.Done()
			id, err := svc.Submit(stressPlan(1000))
			if err != nil {
				t.Errorf("dedupe submit: %v", err)
				return
			}
			dedupeIDs[i] = id
		}(i)
	}
	dedupe.Wait()
	for _, id := range dedupeIDs {
		if id != dedupeIDs[0] {
			t.Fatalf("同一计划得到多个 jobId: %v", dedupeIDs)
		}
	}

	for i, id := range ids {
		if id == "" {
			t.Fatalf("提交 %d 没有返回 id", i)
		}
		st := waitState(t, svc, id)
		switch st.State {
		case StateSucceeded:
			if len(st.Artifacts) == 0 {
				t.Errorf("job %s 成功但没有产物", id)
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, id, st.Artifacts[0])); err != nil {
				t.Errorf("job %s 声称成功但产物不在盘上: %v", id, err)
			}
		case StateCanceled:
			// 目录清理由该 job 自己的 goroutine 完成，是最终一致：取消刚生效时
			// 它可能还在 backend 里，或还在排队等信号量。给一个上界，超了才算泄漏。
			if !waitDirGone(filepath.Join(dir, id), 2*time.Second) {
				t.Errorf("job %s 已取消但目录仍在", id)
			}
		default:
			t.Errorf("job %s 终态 = %s (%s)", id, st.State, st.Error)
		}
	}

	// 信号量必须归零：泄漏会让后续任务永远排队。
	deadline := time.Now().Add(3 * time.Second)
	for len(svc.sem) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("信号量泄漏：仍有 %d 个槽位被占用", len(svc.sem))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitDirGone 轮询等待目录消失（取消后的清理由 job 自己的 goroutine 完成）。
func waitDirGone(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitRunning 等到任务进入 running 状态。
func waitRunning(t *testing.T, svc *Service, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := svc.Status(id); ok && st.State == StateRunning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s 未在 2s 内进入 running", id)
}

// slowPlan 返回一份页数够多、能撑到被取消的计划。
func slowPlan(pages int) *Timeline {
	plan := validPlan()
	base := plan.Pages[0]
	for i := 1; i < pages; i++ {
		p := base
		p.Index = i
		p.SlideID = fmt.Sprintf("slide-%d", i+1)
		plan.Pages = append(plan.Pages, p)
	}
	return plan
}

// stressPlan 造出 hash 互不相同的计划（SlideID 参与 hash）。
// 每份 8 页，撑到足以让取消落在「运行中」而不只是「已完成」。
func stressPlan(i int) *Timeline {
	plan := validPlan()
	base := plan.Pages[0]
	for p := 1; p < 8; p++ {
		pg := base
		pg.Index = p
		pg.SlideID = fmt.Sprintf("stress-%d-%d", i, p)
		plan.Pages = append(plan.Pages, pg)
	}
	plan.Pages[0].SlideID = fmt.Sprintf("stress-%d", i)
	plan.Pages[0].DurationMs = int64(1000 + i)
	return plan
}

// SetFail 切换失败模式：渲染已结束才调用，但仍加锁——测试辅助结构自己也不该是竞态源。
func (b *countingBackend) SetFail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = err
}
