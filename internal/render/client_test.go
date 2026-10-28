package render

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
