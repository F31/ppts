package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type countingBackend struct {
	delay time.Duration
	mu    sync.Mutex
	calls int
	fail  error
}

func (b *countingBackend) Name() string { return "counting" }

func (b *countingBackend) Render(ctx context.Context, plan *Timeline, dir string,
	progress func(done, total int)) error {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	return (&StubBackend{DelayPerPage: b.delay}).Render(ctx, plan, dir, progress)
}

func (b *countingBackend) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func newTestService(t *testing.T, be Backend) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		Backend: be,
		WorkDir: dir,
		Version: "test-1",
		Fonts:   []Font{{Family: "Noto Sans SC", Version: "2.004"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, dir
}

func TestSubmitRendersAndListsArtifacts(t *testing.T) {
	be := &countingBackend{}
	svc, dir := newTestService(t, be)
	id, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, svc, id)
	if st.State != StateSucceeded {
		t.Fatalf("state = %s, err = %s", st.State, st.Error)
	}
	if len(st.Artifacts) != 1 || st.Artifacts[0] != "segment_000.mp4.stub" {
		t.Fatalf("artifacts = %v", st.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(dir, id, "segment_000.mp4.stub")); err != nil {
		t.Fatalf("产物未落盘: %v", err)
	}
}

// TestSubmitIsIdempotent 固定 planHash 幂等：同一计划重复提交不重复渲染。
func TestSubmitIsIdempotent(t *testing.T) {
	be := &countingBackend{}
	svc, _ := newTestService(t, be)

	id1, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("同一计划应返回同一 jobId: %s vs %s", id1, id2)
	}
	waitState(t, svc, id1)
	time.Sleep(20 * time.Millisecond)
	if got := be.Calls(); got != 1 {
		t.Fatalf("backend 调用次数 = %d, want 1", got)
	}
}

func TestSubmitOverHTTP(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body, _ := json.Marshal(validPlan())
	resp, err := http.Post(srv.URL+"/v1/render", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, data)
	}
	var st JobStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.ID == "" || st.State != StateQueued {
		t.Fatalf("响应 = %+v", st)
	}

	// 重复提交：200 且同一 id。
	resp2, err := http.Post(srv.URL+"/v1/render", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("重复提交 status = %d, want 200", resp2.StatusCode)
	}

	// 等任务落终态再返回：worker 还在往工作目录写产物时，t.TempDir 的清理会撞上
	// 「目录非空」（Windows 上表现为这条用例偶发红灯，与业务逻辑无关）。
	if done := waitState(t, svc, st.ID); done.State != StateSucceeded {
		t.Fatalf("final state = %s, err = %s", done.State, done.Error)
	}
}

func TestHTTPRejectsBadPlan(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	bad := validPlan()
	bad.Pages[0].BaseImage = "../../etc/passwd"
	body, _ := json.Marshal(bad)
	resp, err := http.Post(srv.URL+"/v1/render", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var e struct{ Code, Field string }
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Code != CodeUnsafePath || !strings.Contains(e.Field, "baseImage") {
		t.Fatalf("错误体 = %+v", e)
	}
}

func TestHTTPRejectsUnknownField(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	// 手工加一个未知顶层字段：契约漂移必须当场暴露，而不是被静默忽略。
	payload := `{"schemaVersion":"` + IRSchemaVersion + `","fps":30,"width":1920,"height":1080,` +
		`"background":"#fff","pages":[],"script":"alert(1)"}`
	resp, err := http.Post(srv.URL+"/v1/render", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPRejectsOversizedPlan(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		Backend: &countingBackend{}, WorkDir: dir, MaxPlanBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body, _ := json.Marshal(validPlan())
	resp, err := http.Post(srv.URL+"/v1/render", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestCancelStopsJob(t *testing.T) {
	be := &countingBackend{delay: 50 * time.Millisecond}
	svc, _ := newTestService(t, be)
	plan := validPlan()
	// 加上多页让渲染有时间被取消。
	for i := 0; i < 20; i++ {
		p := plan.Pages[0]
		p.Index = i + 1
		p.SlideID = fmt.Sprintf("slide-%d", i+2)
		plan.Pages = append(plan.Pages, p)
	}
	id, err := svc.Submit(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Cancel(id) {
		t.Fatal("取消应返回 true")
	}
	st := waitState(t, svc, id)
	if st.State != StateCanceled {
		t.Fatalf("state = %s, want canceled", st.State)
	}
}

func TestFailedBackend(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{fail: fmt.Errorf("ffmpeg 崩了")})
	id, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, svc, id)
	if st.State != StateFailed || !strings.Contains(st.Error, "ffmpeg") {
		t.Fatalf("state = %s, err = %q", st.State, st.Error)
	}
}

func TestHealthz(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
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
	if h.Backend != "counting" || h.SchemaVersion != IRSchemaVersion || h.Version != "test-1" {
		t.Fatalf("healthz = %+v", h)
	}
	if len(h.Fonts) != 1 || h.Fonts[0].Family != "Noto Sans SC" {
		t.Fatalf("字体清单 = %+v", h.Fonts)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/render")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q", allow)
	}
}

func TestUnknownJob(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/render/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func waitState(t *testing.T, svc *Service, id string) JobStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := svc.Status(id)
		if !ok {
			t.Fatalf("job %s 不存在", id)
		}
		if st.State == StateSucceeded || st.State == StateFailed || st.State == StateCanceled {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s 未在 5s 内结束", id)
	return JobStatus{}
}
