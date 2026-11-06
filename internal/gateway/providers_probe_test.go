package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 探活失败分类的回归。真实故障（2026-11-06，平台共享 LLM 网关 local_llm）：
// 凭据有效、模型 agnes-2.5-flash 确实在上游 /v1/models 里，但账户额度为负，
// flash 系列 chat 请求静默挂到 30s 超时；旧实现只回 "context deadline exceeded"，
// 管理员在模型服务页完全看不出该去充值，还会误以为配置没写进数据库。

const probeTestKey = "sk-secret-test-key"

type probeBehavior struct {
	chatStatus   int
	chatBody     string
	chatHang     bool
	modelsStatus int
	modelsBody   string
}

func newProbeServer(t *testing.T, behavior probeBehavior) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer "+probeTestKey {
				t.Errorf("chat 请求没有带库里配置的凭据：%q", got)
			}
			if behavior.chatHang {
				// httptest.Server.Close 会等 handler 返回；给等待加上限，
				// 免得上游模拟「永不响应」时测试自己也挂着不退出。
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(behavior.chatStatus)
			_, _ = fmt.Fprint(w, behavior.chatBody)
		case "/v1/models":
			if got := r.Header.Get("Authorization"); got != "Bearer "+probeTestKey {
				t.Errorf("models 请求没有带库里配置的凭据：%q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(behavior.modelsStatus)
			_, _ = fmt.Fprint(w, behavior.modelsBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func probeConfig(baseURL, model string) *Config {
	return &Config{
		Name: "local_llm", Kind: KindLLM, BaseURL: baseURL,
		APIKey: probeTestKey, Model: model,
	}
}

// 缩短探活超时，让 httptest 能在毫秒级覆盖挂起分支；不改生产默认值。
func fastProbeTimeouts(t *testing.T) {
	t.Helper()
	oldProbe, oldCatalog := llmProbeTimeout, llmCatalogTimeout
	llmProbeTimeout = 200 * time.Millisecond
	llmCatalogTimeout = 2 * time.Second
	t.Cleanup(func() { llmProbeTimeout, llmCatalogTimeout = oldProbe, oldCatalog })
}

func runProbe(t *testing.T, behavior probeBehavior, model string) (int64, error) {
	t.Helper()
	fastProbeTimeouts(t)
	srv := newProbeServer(t, behavior)
	return Probe(context.Background(), probeConfig(srv.URL, model))
}

func TestProbeLLMSuccessHasNoCode(t *testing.T) {
	_, err := runProbe(t, probeBehavior{chatStatus: 200, chatBody: `{"choices":[]}`}, "agnes-2.5-flash")
	if err != nil {
		t.Fatalf("正常 chat 不该报错: %v", err)
	}
	if code := ProbeCodeOf(err); code != "" {
		t.Fatalf("成功时不应有分类码: %q", code)
	}
}

func TestProbeLLMQuotaExhausted(t *testing.T) {
	_, err := runProbe(t, probeBehavior{
		chatStatus: 403,
		chatBody:   `{"error":{"code":"insufficient_user_quota","message":"Insufficient user quota"}}`,
	}, "agnes-2.5-flash")
	if err == nil {
		t.Fatal("403 配额不足必须报错")
	}
	if got := ProbeCodeOf(err); got != string(ProbeCodeQuota) {
		t.Fatalf("分类码 = %q, want %q", got, ProbeCodeQuota)
	}
	if !strings.Contains(err.Error(), "quota/balance exhausted") || !strings.Contains(err.Error(), "insufficient_user_quota") {
		t.Fatalf("错误里应同时给出结论和上游原文: %v", err)
	}
}

func TestProbeLLMCredentialsRejected(t *testing.T) {
	_, err := runProbe(t, probeBehavior{chatStatus: 401, chatBody: `{"error":{"message":"invalid api key"}}`}, "agnes-2.5-flash")
	if got := ProbeCodeOf(err); got != string(ProbeCodeCredentials) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeCredentials, err)
	}
}

func TestProbeLLMRateLimited(t *testing.T) {
	_, err := runProbe(t, probeBehavior{chatStatus: 429, chatBody: `{"error":{"message":"too many requests"}}`}, "agnes-2.5-flash")
	if got := ProbeCodeOf(err); got != string(ProbeCodeRateLimited) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeRateLimited, err)
	}
}

func TestProbeLLMTimeoutWithValidCatalogReportsNoResponse(t *testing.T) {
	_, err := runProbe(t, probeBehavior{
		chatHang:     true,
		modelsStatus: 200,
		modelsBody:   `{"data":[{"id":"agnes-2.5-flash"},{"id":"agnes-2.5-pro-alpha"}]}`,
	}, "agnes-2.5-flash")
	if got := ProbeCodeOf(err); got != string(ProbeCodeNoResponse) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeNoResponse, err)
	}
	if !strings.Contains(err.Error(), "credentials and model") || !strings.Contains(err.Error(), "quota/balance") {
		t.Fatalf("应说明凭据/模型有效并指向额度或服务状态: %v", err)
	}
	assertKeyNotLeaked(t, err)
}

func TestProbeLLMTimeoutWithUnknownModelReportsModelCode(t *testing.T) {
	_, err := runProbe(t, probeBehavior{
		chatHang:     true,
		modelsStatus: 200,
		modelsBody:   `{"data":[{"id":"agnes-3.0-flash"}]}`,
	}, "agnes-2.5-flash")
	if got := ProbeCodeOf(err); got != string(ProbeCodeModel) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeModel, err)
	}
	if !strings.Contains(err.Error(), "not in upstream catalog") || !strings.Contains(err.Error(), "agnes-3.0-flash") {
		t.Fatalf("应指出模型 ID 不在目录并列出可用模型: %v", err)
	}
}

func TestProbeLLMTimeoutWithRejectedCatalogReportsCredentials(t *testing.T) {
	_, err := runProbe(t, probeBehavior{
		chatHang: true, modelsStatus: 401, modelsBody: `{"error":{"message":"Token not provided"}}`,
	}, "agnes-2.5-flash")
	if got := ProbeCodeOf(err); got != string(ProbeCodeCredentials) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeCredentials, err)
	}
	assertKeyNotLeaked(t, err)
}

func TestProbeLLMCatalogUnreachableKeepsNoResponse(t *testing.T) {
	_, err := runProbe(t, probeBehavior{
		chatHang: true, modelsStatus: 502, modelsBody: `bad gateway`,
	}, "agnes-2.5-flash")
	if got := ProbeCodeOf(err); got != string(ProbeCodeUpstream) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeUpstream, err)
	}
	assertKeyNotLeaked(t, err)
}

func TestProbeCodeOfNilAndPlainError(t *testing.T) {
	if ProbeCodeOf(nil) != "" {
		t.Fatal("nil 错误不该有分类码")
	}
	if ProbeCodeOf(fmt.Errorf("plain error")) != "" {
		t.Fatal("普通错误不该有分类码")
	}
}

func assertKeyNotLeaked(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), probeTestKey) {
		t.Fatalf("错误信息泄露了 API Key: %v", err)
	}
}
