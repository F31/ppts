package gateway

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestProbeTTSCosyVoiceZeroShot(t *testing.T) {
	// CosyVoice 的 provider 由数据库里的 provider 字段决定，而不是靠 base_url 猜。
	// 参考音频来自进程环境（部署资产），探活必须能走完整条链路。
	prompt := filepath.Join(t.TempDir(), "prompt.wav")
	pcm := make([]byte, 24000) // 0.5s 24kHz 静音
	wav := make([]byte, 44+len(pcm))
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+len(pcm)))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], 1)
	binary.LittleEndian.PutUint32(wav[24:28], 24000)
	binary.LittleEndian.PutUint32(wav[28:32], 48000)
	binary.LittleEndian.PutUint16(wav[32:34], 2)
	binary.LittleEndian.PutUint16(wav[34:36], 16)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(pcm)))
	copy(wav[44:], pcm)
	if err := os.WriteFile(prompt, wav, 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	t.Setenv("PPTS_COSYVOICE_PROMPT_WAV", prompt)
	t.Setenv("PPTS_COSYVOICE_PROMPT_TEXT", "希望你以后能够做的比我还好呦。")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference_zero_shot" {
			t.Errorf("path = %q, want /inference_zero_shot", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("X-Sample-Rate", "24000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 24000))
	}))
	defer srv.Close()

	cfg := &Config{Name: "local_cosyvoice", Kind: KindTTS, Provider: ProviderCosyVoice, BaseURL: srv.URL}
	if _, err := Probe(context.Background(), cfg); err != nil {
		t.Fatalf("cosyvoice 探活应成功: %v", err)
	}
}

func assertKeyNotLeaked(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), probeTestKey) {
		t.Fatalf("错误信息泄露了 API Key: %v", err)
	}
}

// ---- 语音网关探活分类 ----
//
// 真实故障（2026-11-07）：本机 Qwen3-TTS 服务被移除后，`local_qwen_tts` 仍是
// 默认语音网关。探活只回了一句 "dial tcp 127.0.0.1:8000: i/o timeout"、probeCode
// 为空，管理面无法给出「服务未启动」这类可行动提示，还会干等 30 秒。
// 下面五条守住「语音侧也要有分类」这件事。

func probeTTSConfig(baseURL string) *Config {
	// httptest 的地址是 127.0.0.1，属于回环地址：TTS 供应商对回环地址免鉴权。
	return &Config{Name: "local_qwen_tts", Kind: KindTTS, BaseURL: baseURL, Voice: "vivian"}
}

// newTTSProbeServer 模拟 /v1/audio/speech 的失败响应。
func newTTSProbeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runTTSProbe(t *testing.T, srvURL string) error {
	t.Helper()
	_, err := Probe(context.Background(), probeTTSConfig(srvURL))
	if err == nil {
		t.Fatal("失败场景必须报错")
	}
	return err
}

func TestProbeTTSUnreachableReportsServiceUnreachable(t *testing.T) {
	// 先起再关，拿一个「已经有地址、但没人监听」的回环端口：连接被拒，
	// 这正是本地语音服务被停掉/删掉时的形态。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	err := runTTSProbe(t, deadURL)
	if got := ProbeCodeOf(err); got != string(ProbeCodeUnreachable) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeUnreachable, err)
	}
	if !strings.Contains(err.Error(), "unreachable") || !strings.Contains(err.Error(), "/v1/audio/speech") {
		t.Fatalf("应指出端点不可达并给出实际地址: %v", err)
	}
}

func TestProbeTTSCredentialsRejected(t *testing.T) {
	srv := newTTSProbeServer(t, http.StatusUnauthorized, `{"error":{"message":"invalid token"}}`)
	err := runTTSProbe(t, srv.URL)
	if got := ProbeCodeOf(err); got != string(ProbeCodeCredentials) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeCredentials, err)
	}
}

func TestProbeTTSRateLimited(t *testing.T) {
	srv := newTTSProbeServer(t, http.StatusTooManyRequests, `{"error":{"message":"too many requests"}}`)
	err := runTTSProbe(t, srv.URL)
	if got := ProbeCodeOf(err); got != string(ProbeCodeRateLimited) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeRateLimited, err)
	}
}

func TestProbeTTSModelMissingReportsModelCode(t *testing.T) {
	srv := newTTSProbeServer(t, http.StatusNotFound, `{"error":{"message":"model not found"}}`)
	err := runTTSProbe(t, srv.URL)
	if got := ProbeCodeOf(err); got != string(ProbeCodeModel) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeModel, err)
	}
}

func TestProbeTTSUpstreamFailureReportsUpstreamCode(t *testing.T) {
	srv := newTTSProbeServer(t, http.StatusBadGateway, `bad gateway`)
	err := runTTSProbe(t, srv.URL)
	if got := ProbeCodeOf(err); got != string(ProbeCodeUpstream) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeUpstream, err)
	}
}

func TestProbeTTSQuotaOnPaymentRequired(t *testing.T) {
	srv := newTTSProbeServer(t, http.StatusPaymentRequired, `{"error":{"message":"insufficient balance"}}`)
	err := runTTSProbe(t, srv.URL)
	if got := ProbeCodeOf(err); got != string(ProbeCodeQuota) {
		t.Fatalf("分类码 = %q, want %q (err=%v)", got, ProbeCodeQuota, err)
	}
}
