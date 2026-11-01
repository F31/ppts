package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// 音色试听是**真实计费调用**，这些用例钉住三件事：
// 1) 非法入参不产生任何上游调用（宁可 400，也不"先合成再说"）；
// 2) 供应商的失败、超时、空音频都不能变成 200（浏览器的静默失败最难查）；
// 3) 返回的头能回答"这次到底是哪个模型、多长"，否则前端只能猜。

type fakePreviewTTS struct {
	caps     tts.VoiceCapabilities
	capsErr  error
	result   tts.SynthesisResult
	err      error
	requests []tts.SynthesisRequest
}

func (f *fakePreviewTTS) Capabilities(context.Context, string) (tts.VoiceCapabilities, error) {
	if f.capsErr != nil {
		return tts.VoiceCapabilities{}, f.capsErr
	}
	if f.caps.MaxInputChars == 0 {
		return tts.VoiceCapabilities{MaxInputChars: 5000}, nil
	}
	return f.caps, nil
}

func (f *fakePreviewTTS) Synthesize(_ context.Context, req tts.SynthesisRequest) (tts.SynthesisResult, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return tts.SynthesisResult{}, f.err
	}
	return f.result, nil
}

type previewGatewayStore struct {
	*fakeGatewayStore
	cfg *gateway.Config
	err error
}

func (s *previewGatewayStore) Resolve(context.Context, string, gateway.Kind) (*gateway.Config, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func voicePreviewServer(t *testing.T, provider tts.TTSProvider, gws gateway.StoreResolver, role membership.Role) *httptest.Server {
	t.Helper()
	projects := &fakeProjectStore{projects: []*project.Project{
		{ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "Demo"},
	}}
	h := NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Members: &fakeRoleReader{role: role}, Gateway: gws, DevHeaders: true, VoicePreviewProvider: provider})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func postVoicePreview(t *testing.T, srv *httptest.Server, body any, headers map[string]string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/projects/project-1/voice-preview", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeErrorCode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	return payload
}

func TestVoicePreviewSynthesizesOnceAndReportsWhatWasUsed(t *testing.T) {
	provider := &fakePreviewTTS{result: tts.SynthesisResult{
		Audio: []byte("RIFF....WAVE"), Format: "audio/wav", RealDurationMS: 1234,
	}}
	srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)

	resp := postVoicePreview(t, srv, map[string]any{"voice": "FunAudioLLM/CosyVoice2-0.5B:alex", "ratePercent": 130}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %v", resp.StatusCode, decodeErrorCode(t, resp))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "RIFF....WAVE" {
		t.Fatalf("body = %q，期望原样返回音频字节", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type = %q want audio/wav", ct)
	}
	// 试听结果不该被任何中间层缓存：换音色必须立刻换声音。
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q want no-store", cc)
	}
	if got := resp.Header.Get("X-PPTS-Audio-Duration-Ms"); got != "1234" {
		t.Errorf("duration header = %q want 1234", got)
	}
	if got := resp.Header.Get("X-PPTS-Preview-Model"); got != "" {
		t.Errorf("未经网关解析时不应谎报模型名，得到 %q", got)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("供应商调用次数 = %d want 1", len(provider.requests))
	}
	req := provider.requests[0]
	if req.VoiceID != "FunAudioLLM/CosyVoice2-0.5B:alex" || req.SpeechControl.RatePercent != 130 {
		t.Errorf("合成请求 = %+v，音色/语速未按入参传递", req)
	}
	if req.Text != voicePreviewDefaultZH {
		t.Errorf("未传文本时应使用中文示例句，得到 %q", req.Text)
	}
}

func TestVoicePreviewUsesEnglishSampleForEnglishUI(t *testing.T) {
	provider := &fakePreviewTTS{result: tts.SynthesisResult{Audio: []byte("x"), Format: "audio/wav"}}
	srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)

	resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, map[string]string{"X-PPTS-Language": "en-US"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := provider.requests[0].Text; got != voicePreviewDefaultEN {
		t.Errorf("英文界面示例句 = %q，期望英文", got)
	}
	if got := provider.requests[0].Language; got != "en-US" {
		t.Errorf("语言 = %q want en-US", got)
	}
}

// 非法入参必须在调用供应商之前就被拒绝：试听是计费的，不能"先合成再报错"。
func TestVoicePreviewRejectsInvalidInputBeforeCallingProvider(t *testing.T) {
	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"缺音色", map[string]any{"voice": "  "}, http.StatusBadRequest, "invalid_argument"},
		{"语速过低", map[string]any{"voice": "v", "ratePercent": 10}, http.StatusBadRequest, "invalid_argument"},
		{"语速过高", map[string]any{"voice": "v", "ratePercent": 400}, http.StatusBadRequest, "invalid_argument"},
		{"文本超长", map[string]any{"voice": "v", "text": strings.Repeat("字", voicePreviewMaxRunes+1)}, http.StatusBadRequest, "text_too_long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakePreviewTTS{result: tts.SynthesisResult{Audio: []byte("x")}}
			srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)
			resp := postVoicePreview(t, srv, tc.body, nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d want %d", resp.StatusCode, tc.status)
			}
			if payload := decodeErrorCode(t, resp); payload["code"] != tc.code {
				t.Errorf("code = %v want %s", payload["code"], tc.code)
			}
			if len(provider.requests) != 0 {
				t.Errorf("非法入参仍调用了供应商 %d 次", len(provider.requests))
			}
		})
	}
}

func TestVoicePreviewRejectsTextBeyondVoiceLimit(t *testing.T) {
	provider := &fakePreviewTTS{caps: tts.VoiceCapabilities{MaxInputChars: 4}}
	srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)

	resp := postVoicePreview(t, srv, map[string]any{"voice": "v", "text": "一二三四五"}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", resp.StatusCode)
	}
	if payload := decodeErrorCode(t, resp); payload["code"] != "text_too_long" {
		t.Errorf("code = %v want text_too_long", payload["code"])
	}
	if len(provider.requests) != 0 {
		t.Errorf("超出音色上限仍调用了供应商")
	}
}

func TestVoicePreviewReportsMissingGatewayAsConfigGap(t *testing.T) {
	// 没有网关 ≠ 网关故障：前端要据此提示"去配模型"，而不是"重试"。
	srv := voicePreviewServer(t, nil, &previewGatewayStore{err: gateway.ErrNotFound}, membership.RoleEditor)
	resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503", resp.StatusCode)
	}
	if payload := decodeErrorCode(t, resp); payload["code"] != "tts_not_configured" {
		t.Errorf("code = %v want tts_not_configured", payload["code"])
	}
}

func TestVoicePreviewRejectsGatewayWithoutKey(t *testing.T) {
	// 有网关但没密钥：同样属于配置缺项，不该把 401/403 说成"供应商故障"。
	store := &previewGatewayStore{cfg: &gateway.Config{Model: "m", APIKey: ""}}
	srv := voicePreviewServer(t, nil, store, membership.RoleEditor)
	resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503", resp.StatusCode)
	}
	if payload := decodeErrorCode(t, resp); payload["code"] != "tts_not_configured" {
		t.Errorf("code = %v want tts_not_configured", payload["code"])
	}
}

func TestVoicePreviewUsesResolvedGatewayModel(t *testing.T) {
	// 试听必须走与配音 worker 相同的缺省网关解析，并把实际模型回给前端，
	// 否则"试听听到的"与"成片用的"可能不是同一个模型，而界面上看不出来。
	store := &previewGatewayStore{cfg: &gateway.Config{Name: "default", Model: "FunAudioLLM/CosyVoice2-0.5B", APIKey: "k"}}
	projects := &fakeProjectStore{projects: []*project.Project{{ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1"}}}
	var built *gateway.Config
	deps := voicePreviewDeps{
		gateways: store,
		projects: projects,
		buildProvider: func(cfg *gateway.Config) tts.TTSProvider {
			built = cfg
			return &fakePreviewTTS{result: tts.SynthesisResult{Audio: []byte("x"), Format: "audio/wav", RealDurationMS: 700}}
		},
	}
	// 用真实路由覆盖 buildProvider 会太绕；这里直接调 handler，等价于路由挂载后的行为。
	req := httptest.NewRequest(http.MethodPost, "/projects/project-1/voice-preview", strings.NewReader(`{"voice":"v"}`))
	req.SetPathValue("pid", "project-1")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"}))
	rec := httptest.NewRecorder()
	voicePreview(rec, req, deps)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if built == nil || built.Model != "FunAudioLLM/CosyVoice2-0.5B" {
		t.Fatalf("构建供应商用的配置 = %+v，未使用网关解析结果", built)
	}
	if got := rec.Header().Get("X-PPTS-Preview-Model"); got != "FunAudioLLM/CosyVoice2-0.5B" {
		t.Errorf("模型头 = %q，期望回显实际使用的模型", got)
	}
}

func TestVoicePreviewMapsProviderFailuresToBadGateway(t *testing.T) {
	cases := []struct {
		name string
		err  error
		body string
	}{
		{"供应商报错", errors.New("upstream 500: internal\ntrace"), ""},
		{"返回空音频", nil, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakePreviewTTS{err: tc.err}
			if tc.body == "empty" {
				provider.result = tts.SynthesisResult{Format: "audio/wav"} // 0 字节
			}
			srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)
			resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, nil)
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d want 502", resp.StatusCode)
			}
			payload := decodeErrorCode(t, resp)
			if payload["code"] != "tts_failed" {
				t.Errorf("code = %v want tts_failed", payload["code"])
			}
			msg, _ := payload["message"].(string)
			if strings.Contains(msg, "\n") {
				t.Errorf("错误信息未压成单行：%q", msg)
			}
		})
	}
}

func TestVoicePreviewErrorTextStaysValidUTF8(t *testing.T) {
	// 上游错误可能是中文长串：按字节截断会把字符砍成半个，JSON 里变成乱码。
	provider := &fakePreviewTTS{err: errors.New(strings.Repeat("供应商返回了无法解析的响应内容", 40))}
	srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)
	resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d want 502", resp.StatusCode)
	}
	msg, _ := decodeErrorCode(t, resp)["message"].(string)
	if !utf8.ValidString(msg) {
		t.Fatalf("错误文本不是合法 UTF-8：%q", msg)
	}
	if n := utf8.RuneCountInString(msg); n > 200 {
		t.Errorf("错误文本 %d 个字符，应截断到 200 以内", n)
	}
	if strings.ContainsRune(msg, '\uFFFD') {
		t.Errorf("错误文本含替换符（截断砍坏了字符）：%q", msg)
	}
}

func TestVoicePreviewMapsDeadlineToGatewayTimeout(t *testing.T) {
	// 超时与供应商报错的可操作结论不同（等一下再试 vs 换配置），必须分开。
	provider := &fakePreviewTTS{err: context.DeadlineExceeded}
	srv := voicePreviewServer(t, provider, nil, membership.RoleEditor)
	resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, nil)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d want 504", resp.StatusCode)
	}
	if payload := decodeErrorCode(t, resp); payload["code"] != "tts_timeout" {
		t.Errorf("code = %v want tts_timeout", payload["code"])
	}
}

func TestVoicePreviewRequiresEditorRole(t *testing.T) {
	// 试听会真花钱：只读成员不该拿别人的额度做实验。
	provider := &fakePreviewTTS{result: tts.SynthesisResult{Audio: []byte("x")}}
	srv := voicePreviewServer(t, provider, nil, membership.RoleViewer)
	resp := postVoicePreview(t, srv, map[string]any{"voice": "v"}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d want 403", resp.StatusCode)
	}
	if len(provider.requests) != 0 {
		t.Errorf("无权限请求仍调用了供应商")
	}
}

// previewModelStore 造一个同时有两个模型行的网关存储：
// 默认行是 model-a，另一行是 model-b。用于验证"请求点名哪个就用哪个"。
func previewModelStore() *previewGatewayStore {
	return &previewGatewayStore{
		fakeGatewayStore: &fakeGatewayStore{rows: map[string]*gateway.Gateway{
			gwKey("tenant-1", "gw-a", "tts"): {
				TenantID: "tenant-1", Name: "gw-a", Kind: gateway.KindTTS,
				Model: "model-a", Enabled: true, IsDefault: true,
			},
			gwKey("tenant-1", "gw-b", "tts"): {
				TenantID: "tenant-1", Name: "gw-b", Kind: gateway.KindTTS,
				Model: "model-b", Enabled: true,
			},
		}},
		cfg: &gateway.Config{Name: "gw-a", Model: "model-a", APIKey: "k"},
	}
}

// TestVoicePreviewUsesRequestedModel 守护试听与生成"听的是同一个模型"。
//
// 修复前试听只认默认网关：下拉里选了 model-b，听到的仍是 model-a 的样音，
// 而界面标签写着 model-b。这种偏差用户无法从声音里判断（两个模型可能只是音色略有差异），
// 但会直接导致"试听满意、成片不对"。
func TestVoicePreviewUsesRequestedModel(t *testing.T) {
	store := previewModelStore()
	projects := &fakeProjectStore{projects: []*project.Project{{ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1"}}}
	var built *gateway.Config
	deps := voicePreviewDeps{
		gateways: store,
		projects: projects,
		buildProvider: func(cfg *gateway.Config) tts.TTSProvider {
			built = cfg
			return &fakePreviewTTS{result: tts.SynthesisResult{Audio: []byte("x"), Format: "audio/wav", RealDurationMS: 700}}
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/projects/project-1/voice-preview", strings.NewReader(`{"voice":"v","model":"model-b"}`))
	req.SetPathValue("pid", "project-1")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"}))
	rec := httptest.NewRecorder()
	voicePreview(rec, req, deps)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if built == nil || built.Model != "model-b" {
		t.Fatalf("构建供应商用的配置 = %+v，未使用请求点名的 model-b", built)
	}
	if got := rec.Header().Get("X-PPTS-Preview-Model"); got != "model-b" {
		t.Errorf("模型头 = %q，期望回显本次实际使用的模型", got)
	}
}

// TestVoicePreviewRejectsUnknownModel 守护"点名不存在的模型不许用默认的顶上"。
// 若这里悄悄回落 model-a，用户听到的样音与所选选项无关，且响应里还会写 model-a——
// 界面若照实显示，用户会以为下拉失效；若不显示，就是无声的假成功。
func TestVoicePreviewRejectsUnknownModel(t *testing.T) {
	store := previewModelStore()
	projects := &fakeProjectStore{projects: []*project.Project{{ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1"}}}
	called := false
	deps := voicePreviewDeps{
		gateways: store,
		projects: projects,
		buildProvider: func(cfg *gateway.Config) tts.TTSProvider {
			called = true
			return &fakePreviewTTS{}
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/projects/project-1/voice-preview", strings.NewReader(`{"voice":"v","model":"model-missing"}`))
	req.SetPathValue("pid", "project-1")
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"}))
	rec := httptest.NewRecorder()
	voicePreview(rec, req, deps)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s want 400", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v (body=%s)", err, rec.Body.String())
	}
	if payload["code"] != "model_not_configured" {
		t.Errorf("code = %v want model_not_configured", payload["code"])
	}
	if called {
		t.Errorf("模型不存在时不得构建供应商（会真的合成一段默认模型的样音）")
	}
}
