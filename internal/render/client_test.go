package render

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{BaseURL: baseURL, Retries: 2, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClientRoundTrip(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if !c.Available(context.Background()) {
		t.Fatal("健康检查应通过")
	}
	id, err := c.Submit(context.Background(), validPlan())
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Wait(context.Background(), id, 5*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateSucceeded {
		t.Fatalf("state = %s, err = %s", st.State, st.Error)
	}
	if len(st.Artifacts) != 1 {
		t.Fatalf("artifacts = %v", st.Artifacts)
	}
}

func TestClientDoesNotRetryOn400(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_plan","field":"pages[0].durationMs","message":"bad"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Submit(context.Background(), validPlan())
	if err == nil {
		t.Fatal("应返回错误")
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 || he.Field != "pages[0].durationMs" {
		t.Fatalf("err = %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("400 不应重试，实际请求 %d 次", got)
	}
	if IsUnavailable(err) {
		t.Fatal("校验失败不属于服务不可用，不应触发降级")
	}
}

func TestClientRetriesOn503(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"busy","message":"渲染器忙"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Submit(context.Background(), validPlan())
	if err == nil {
		t.Fatal("应返回错误")
	}
	// 1 次首发 + 2 次重试。
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("503 应重试 2 次（共 3 次请求），实际 %d", got)
	}
	if !IsUnavailable(err) {
		t.Fatalf("503 应视为可降级，err = %v", err)
	}
}

func TestClientUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 立刻关掉：连接被拒绝。

	c, err := NewClient(ClientConfig{BaseURL: url, Retries: 1, Backoff: time.Millisecond,
		HTTPClient: &http.Client{Timeout: 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Submit(context.Background(), validPlan())
	if !errors.Is(err, ErrAnimationUnavailable) {
		t.Fatalf("err = %v, want ErrAnimationUnavailable", err)
	}
	if !IsUnavailable(err) {
		t.Fatal("连接失败必须触发降级")
	}
}

func TestClientSchemaMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Health{Version: "x", SchemaVersion: "compiled-timeline/v2"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if c.Available(context.Background()) {
		t.Fatal("契约版本不一致时应视为不可用")
	}
}

func TestRequireFonts(t *testing.T) {
	svc, _ := newTestService(t, &countingBackend{})
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.RequireFonts(context.Background(), "Noto Sans SC"); err != nil {
		t.Fatalf("字体存在却报错: %v", err)
	}
	err := c.RequireFonts(context.Background(), "Microsoft YaHei")
	if !errors.Is(err, ErrFontUnavailable) {
		t.Fatalf("err = %v, want ErrFontUnavailable", err)
	}
	// 缺字体不是"服务不可用"：应该明确失败，而不是静默降级到方框字。
	if IsUnavailable(err) {
		t.Fatal("缺字体不应被当作服务不可用")
	}
}

func TestClientValidatesLocally(t *testing.T) {
	// 本地校验就失败的计划不该产生任何网络请求。
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	bad := validPlan()
	bad.FPS = 1
	if _, err := c.Submit(context.Background(), bad); err == nil {
		t.Fatal("本地校验应失败")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("不应发起网络请求，实际 %d 次", got)
	}
}

// TestClientCheckBypassesHealthCache 守护 Check 与 Health 的**语义差别**：
//
// Health 的结论按进程缓存一次（导出主路径要的是"别因抖动来回切"），
// Check 每次都真探（导出对话框要的是"现在能不能用"）。
//
// 若 Check 复用了 Health 的缓存，这条用例会在第 3 步失败：服务已经 500 了，
// Check 仍然报可用——用户修好服务后重启前一直置灰的问题会反向变成
// "服务挂了仍然显示可用"。
func TestClientCheckBypassesHealthCache(t *testing.T) {
	var fail atomic.Bool
	health := `{"backend":"stub","schemaVersion":"` + IRSchemaVersion + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(health))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("首次 Check 应通过: %v", err)
	}
	// 让 Health 的缓存先建立（此后它对成功的判断不再改变）。
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health 应通过: %v", err)
	}
	fail.Store(true)
	if err := c.Check(context.Background()); err == nil {
		t.Fatal("服务已 500，Check 仍报可用（说明它吃了 Health 的进程缓存）")
	}
	if !c.Available(context.Background()) {
		t.Fatal("Health 的结论应保持首次结果——两处语义不能互相污染")
	}
}

// TestCheckDoesNotDoubleWrapUnavailable 守用户可见的那条错误文案。
//
// Check 与 do 都会把连接层失败包成 ErrAnimationUnavailable，两层叠加的产物是
// "animation service unavailable: animation service unavailable: Get …"，
// 而这段字符串会原样出现在导出对话框里——重复前缀会让人以为有两个独立故障。
func TestCheckDoesNotDoubleWrapUnavailable(t *testing.T) {
	c, err := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1", Retries: 0, Timeout: time.Second})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	err = c.Check(context.Background())
	if err == nil {
		t.Fatal("连不上时必须返回错误，否则导出会以为自己能出动画")
	}
	if !errors.Is(err, ErrAnimationUnavailable) {
		t.Fatalf("error = %v, want ErrAnimationUnavailable", err)
	}
	if n := strings.Count(err.Error(), "animation service unavailable"); n != 1 {
		t.Fatalf("错误前缀出现了 %d 次: %v", n, err)
	}
}
