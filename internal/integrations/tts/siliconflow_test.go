package tts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// smallWAV 生成 1 秒、16000Hz、16bit mono 的静音 PCM WAV。
func smallWAV(t *testing.T) []byte {
	t.Helper()
	sampleRate := uint32(16000)
	dataSize := uint32(sampleRate * 2) // 1 秒
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	out[4], out[5], out[6], out[7] = byte((36+dataSize)&0xff), byte((36+dataSize)>>8&0xff), byte((36+dataSize)>>16&0xff), byte((36+dataSize)>>24&0xff)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	out[16], out[17], out[18], out[19] = 16, 0, 0, 0
	out[20], out[21] = 1, 0 // PCM
	out[22], out[23] = 1, 0 // mono
	out[24] = byte(sampleRate & 0xff)
	out[25] = byte(sampleRate >> 8 & 0xff)
	out[26] = byte(sampleRate >> 16 & 0xff)
	out[27] = byte(sampleRate >> 24 & 0xff)
	byteRate := sampleRate * 2
	out[28] = byte(byteRate & 0xff)
	out[29] = byte(byteRate >> 8 & 0xff)
	out[30] = byte(byteRate >> 16 & 0xff)
	out[31] = byte(byteRate >> 24 & 0xff)
	out[32], out[33] = 2, 0  // block align
	out[34], out[35] = 16, 0 // bits
	copy(out[36:40], "data")
	out[40] = byte(dataSize & 0xff)
	out[41] = byte(dataSize >> 8 & 0xff)
	out[42] = byte(dataSize >> 16 & 0xff)
	out[43] = byte(dataSize >> 24 & 0xff)
	return out
}

func TestSiliconFlowSynthesizeSuccess(t *testing.T) {
	var gotAuth string
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model  string `json:"model"`
			Input  string `json:"input"`
			Voice  string `json:"voice"`
			Format string `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		gotModel = body.Model
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(smallWAV(t))
	}))
	defer srv.Close()

	p := NewSiliconFlowProvider(SiliconFlowConfig{
		BaseURL: srv.URL, APIKey: "sk-test", Model: "m", Voice: "v-1",
	})
	res, err := p.Synthesize(context.Background(), SynthesisRequest{
		LogicalOpID: "op-1", VoiceID: "", Text: "你好世界", Language: "zh-CN",
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotModel != "m" {
		t.Fatalf("model = %q", gotModel)
	}
	if res.Format != "audio/wav" || res.RealDurationMS != 1000 {
		t.Fatalf("result = %+v", res)
	}
	if res.Alignment == nil || res.Alignment.Text != "你好世界" || res.Alignment.Method != AlignEstimate {
		t.Fatalf("alignment = %+v", res.Alignment)
	}
	prev := int64(0)
	for _, tok := range res.Alignment.Tokens {
		if tok.StartUS < prev || tok.EndUS <= tok.StartUS || tok.EndUS > res.RealDurationMS*1000 {
			t.Fatalf("bad token: %+v", tok)
		}
		prev = tok.EndUS
	}
	if len(res.Audio) == 0 || res.BillingUnit != "char" {
		t.Fatalf("audio/billing missing")
	}
}

func TestSiliconFlowSynthesizeDefaultsVoice(t *testing.T) {
	var gotVoice string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Voice string `json:"voice"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotVoice = body.Voice
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(smallWAV(t))
	}))
	defer srv.Close()

	p := NewSiliconFlowProvider(SiliconFlowConfig{
		BaseURL: srv.URL, APIKey: "sk-test", Model: "FunAudioLLM/CosyVoice2-0.5B",
	})
	if _, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "你好", VoiceID: ""}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if gotVoice != "FunAudioLLM/CosyVoice2-0.5B:alex" {
		t.Fatalf("voice = %q, want model:alex", gotVoice)
	}
}

func TestSiliconFlowRetries429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()
	p := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: srv.URL, APIKey: "sk-test"})
	_, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "hi", VoiceID: "v"})
	var retryable *RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("expected RetryableError, got %v", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error should include status: %v", err)
	}
}

func TestSiliconFlowMissingAPIKey(t *testing.T) {
	p := NewSiliconFlowProvider(SiliconFlowConfig{})
	if _, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "hi", VoiceID: "v"}); err == nil {
		t.Fatalf("missing api key should error")
	}
}

func TestWAVDurationMS(t *testing.T) {
	w := smallWAV(t)
	d, err := wavDurationMSFromRIFF(w)
	if err != nil {
		t.Fatalf("wavDurationMSFromRIFF: %v", err)
	}
	if d != 1000 {
		t.Fatalf("duration = %d want 1000", d)
	}
	if _, err := wavDurationMSFromRIFF([]byte("not-wav")); err == nil {
		t.Fatalf("invalid wav should error")
	}
}

// bogusSizeWAV 模拟 SiliconFlow：RIFF/data 长度写占位大值，实际数据到 EOF。
func bogusSizeWAV(t *testing.T) []byte {
	t.Helper()
	base := smallWAV(t)
	// 头部 data 大小写 0xffffff00（占位），实际数据长度不变。
	base[40], base[41], base[42], base[43] = 0x00, 0xff, 0xff, 0xff
	return base
}

func TestNormalizeWAV16FixesBogusDataSize(t *testing.T) {
	raw := bogusSizeWAV(t)
	norm, err := normalizeWAV16(raw)
	if err != nil {
		t.Fatalf("normalizeWAV16: %v", err)
	}
	d, err := wavDurationMSFromRIFF(norm)
	if err != nil {
		t.Fatalf("parse normalized: %v", err)
	}
	if d != 1000 {
		t.Fatalf("normalized duration = %d want 1000", d)
	}
	if len(norm) != len(raw) {
		t.Fatalf("normalized len = %d want %d", len(norm), len(raw))
	}
	// data 大小字段应修正为真实值 32000 = 0x7d00。
	if norm[40] != 0x00 || norm[41] != 0x7d || norm[42] != 0x00 || norm[43] != 0x00 {
		t.Fatalf("data size bytes not corrected: % x", norm[40:44])
	}
}

func TestBuildEstimatedAlignmentBounds(t *testing.T) {
	a := buildEstimatedAlignment("测试文本", 2000)
	if a.Method != AlignEstimate || len(a.Tokens) != 4 {
		t.Fatalf("alignment = %+v", a)
	}
	for _, tok := range a.Tokens {
		if tok.EndUS <= tok.StartUS || tok.EndUS > 2000*1000 {
			t.Fatalf("token out of bounds: %+v", tok)
		}
	}
}

func TestSiliconFlowCapabilities(t *testing.T) {
	p := NewSiliconFlowProvider(SiliconFlowConfig{})
	c, err := p.Capabilities(context.Background(), "voice-x")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if c.VoiceID != "voice-x" || c.ModelID == "" || len(c.Languages) == 0 {
		t.Fatalf("capabilities = %+v", c)
	}
}

// TestSiliconFlowLiveSmoke 仅在设置了 PPTS_TTS_API_KEY 时对真实 API 做一次合成冒烟。
// 用于接入正式供应商后的实机验收；本地门禁默认 Skip。
func TestSiliconFlowLiveSmoke(t *testing.T) {
	key := os.Getenv("PPTS_TTS_API_KEY")
	if key == "" {
		t.Skip("PPTS_TTS_API_KEY not set")
	}
	p := NewSiliconFlowProvider(SiliconFlowConfig{
		APIKey: key,
		Model:  os.Getenv("PPTS_TTS_MODEL"),
		Voice:  os.Getenv("PPTS_TTS_VOICE"),
	})
	res, err := p.Synthesize(context.Background(), SynthesisRequest{
		LogicalOpID: "smoke", VoiceID: "", Text: "你好，这是一段语音合成验证。", Language: "zh-CN",
	})
	if err != nil {
		t.Fatalf("live smoke: %v", err)
	}
	if len(res.Audio) == 0 || res.RealDurationMS <= 0 || res.Alignment == nil || res.Alignment.Method != AlignEstimate {
		t.Fatalf("live smoke result = %+v", res)
	}
}

// TestSiliconFlowInvalidVoiceShowsSentValue 守护「报错要能自解释」：
// 供应商只回 {"code":20047,"message":"Invalid voice"}，用户不知道是我们发了什么。
// 真实事故：音色写成 "FunAudioLLM/CosyVoice2-0.5B.alex"（点号），只看到 Invalid voice。
func TestSiliconFlowInvalidVoiceShowsSentValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":20047,"message":"Invalid voice","data":null}`))
	}))
	defer srv.Close()

	p := NewSiliconFlowProvider(SiliconFlowConfig{
		BaseURL: srv.URL, APIKey: "sk-test", Model: "FunAudioLLM/CosyVoice2-0.5B",
	})
	const sent = "FunAudioLLM/CosyVoice2-0.5B.alex"
	_, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "hi", VoiceID: sent})
	if err == nil {
		t.Fatal("invalid voice should fail")
	}
	msg := err.Error()
	for _, want := range []string{sent, "<model>:<voice>", "20047"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error should contain %q, got: %s", want, msg)
		}
	}
}

// TestSiliconFlowOtherErrorsCarryNoVoiceHint：只有"音色被拒"才补格式提示。
// 401/429 也带这句话会把用户引向错误方向（去改音色，而问题在密钥/限流）。
func TestSiliconFlowOtherErrorsCarryNoVoiceHint(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"invalid token", http.StatusUnauthorized, `{"code":20001,"message":"Invalid token"}`},
		{"bad request other code", http.StatusBadRequest, `{"code":20003,"message":"Invalid model"}`},
		{"rate limited", http.StatusTooManyRequests, `{"code":20015,"message":"rate limited"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: srv.URL, APIKey: "sk-test"})
			_, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "hi", VoiceID: "v"})
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "sent voice=") {
				t.Fatalf("%s should not carry voice hint: %s", tc.name, err)
			}
		})
	}
}

// 本地语音模型（Qwen3-TTS / liteaig 一类）不校验 API Key。空 key 只对
// http + loopback 放行，且不能伪造一个 "Bearer " 头出去。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIsLoopbackBaseURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:8000", true},
		{"http://127.0.0.2:18080", true},
		{"http://localhost:8000/v1", true},
		{"http://[::1]:8000", true},
		{"https://127.0.0.1:8000", false},
		{"https://api.siliconflow.cn", false},
		{"http://192.168.1.10:8000", false},
		{"http://host.docker.internal:8000", false},
		{"", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		if got := IsLoopbackBaseURL(tc.url); got != tc.want {
			t.Errorf("IsLoopbackBaseURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestSiliconFlowAnonymousLocalSynthesize(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(smallWAV(t))
	}))
	defer srv.Close()

	p := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: srv.URL, Model: "qwen3-tts-1.7b-customvoice", Voice: "vivian"})
	res, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "本地语音模型", VoiceID: ""})
	if err != nil {
		t.Fatalf("本地免鉴权网关合成失败: %v", err)
	}
	if len(res.Audio) == 0 {
		t.Fatal("返回音频为空")
	}
	if gotAuth != "" {
		t.Fatalf("空 key 不应发送 Authorization，实际 %q", gotAuth)
	}
}

func TestSiliconFlowEmptyKeyStillRejectedForRemote(t *testing.T) {
	calls := 0
	p := NewSiliconFlowProvider(SiliconFlowConfig{
		BaseURL: "https://api.siliconflow.cn", Model: "m",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})},
	})
	_, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "hi", VoiceID: "v"})
	if err == nil || !strings.Contains(err.Error(), "api key not configured") {
		t.Fatalf("远程网关空 key 必须报错，实际 %v", err)
	}
	if calls != 0 {
		t.Fatalf("空 key 不应发出请求，实际发出 %d 次", calls)
	}
}

func TestSiliconFlowLocalWithKeyStillSendsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(smallWAV(t))
	}))
	defer srv.Close()

	p := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: srv.URL, APIKey: "local-token", Model: "m"})
	if _, err := p.Synthesize(context.Background(), SynthesisRequest{Text: "hi", VoiceID: "v"}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if gotAuth != "Bearer local-token" {
		t.Fatalf("本地网关若配了 key 仍应带 Authorization，实际 %q", gotAuth)
	}
}
