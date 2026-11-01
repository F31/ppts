package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/migrations"
)

// 这条用例走**真链路**：真实网关存储（SQLite + AES-GCM 密钥解密）→ 真实
// SiliconFlow 适配器 → 真实 HTTP 调用到假上游 → 真实 WAV 解码。
//
// 单测里注入 fake provider 只能证明"handler 会转发"，证明不了
// "网关里配的 model/音色真的被用上、密钥真的解出来了、返回的时长是真解码出来的"。
// 这三件事任何一件错了，用户听到的都是错的声音或根本听不到。

type ttsUpstreamCall struct {
	Path  string
	Auth  string
	Model string
	Input string
	Voice string
	Speed float64
}

type fakeTTSUpstream struct {
	mu      sync.Mutex
	calls   []ttsUpstreamCall
	status  int
	payload []byte
}

func (f *fakeTTSUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string  `json:"model"`
		Input string  `json:"input"`
		Voice string  `json:"voice"`
		Speed float64 `json:"speed"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.calls = append(f.calls, ttsUpstreamCall{
		Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
		Model: body.Model, Input: body.Input, Voice: body.Voice, Speed: body.Speed,
	})
	f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"message":"upstream rejected"}`))
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(f.payload)
}

func (f *fakeTTSUpstream) snapshot() []ttsUpstreamCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ttsUpstreamCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// pcmWAV 生成一段 PCM16 单声道静音 WAV，时长可控 —— 供应商返回的时长是否被
// 正确解码（而不是按字数估）就靠它对照。
func pcmWAV(durationMS, sampleRate int) []byte {
	samples := sampleRate * durationMS / 1000
	out := make([]byte, 44+samples*2)
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+samples*2))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(samples*2))
	return out
}

func TestVoicePreviewRealGatewayChain(t *testing.T) {
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	key := sha256.Sum256([]byte("voice-preview-chain"))
	cipher, err := tenant.NewAESGCMCipher(key[:])
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}

	upstream := &fakeTTSUpstream{payload: pcmWAV(1200, 16000)}
	fakeTTS := httptest.NewServer(upstream)
	t.Cleanup(fakeTTS.Close)

	store := gateway.NewSQLiteStore(sqldb, cipher)
	if err := store.Create(ctx, &gateway.Gateway{
		TenantID: db.LocalTenantID, Name: "tts-main", Kind: gateway.KindTTS,
		Provider: "openai_compatible", BaseURL: fakeTTS.URL,
		Model: "test-tts-model", IsDefault: true, Enabled: true,
	}, "test-key"); err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	projects := &fakeProjectStore{projects: []*project.Project{
		{ID: "project-1", TenantID: db.LocalTenantID, OwnerUser: db.LocalUserID, Title: "Demo"},
	}}
	h := NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Gateway: store, Members: &fakeRoleReader{role: membership.RoleEditor}, DevHeaders: true})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	post := func(body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/projects/project-1/voice-preview", strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(tenantHeader, db.LocalTenantID)
		req.Header.Set(userHeader, db.LocalUserID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	// 1) 正常试听：网关解出密钥 → 适配器打到上游 → 解码真实时长。
	resp := post(`{"voice":"FunAudioLLM/CosyVoice2-0.5B:alex","ratePercent":130}`)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, raw)
	}
	audio, _ := io.ReadAll(resp.Body)
	if !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Errorf("返回体不是 WAV：前 4 字节 = %q", audio[:4])
	}
	if got := resp.Header.Get("Content-Type"); got != "audio/wav" {
		t.Errorf("Content-Type = %q want audio/wav", got)
	}
	// 时长必须来自解码，而不是"字数 ÷ 语速"的估算：1200ms 是上游给的。
	if got := resp.Header.Get("X-PPTS-Audio-Duration-Ms"); got != "1200" {
		t.Errorf("duration = %q want 1200（应解码上游 WAV，而不是按字数估算）", got)
	}
	if got := resp.Header.Get("X-PPTS-Preview-Model"); got != "test-tts-model" {
		t.Errorf("model = %q want test-tts-model（应为网关配置里的模型）", got)
	}

	calls := upstream.snapshot()
	if len(calls) != 1 {
		t.Fatalf("上游调用次数 = %d want 1", len(calls))
	}
	call := calls[0]
	if call.Path != "/v1/audio/speech" {
		t.Errorf("上游路径 = %q", call.Path)
	}
	// 密钥确实从加密列里解出来了；解错会表现成上游 401，而 401 会被包装成"供应商故障"。
	if call.Auth != "Bearer test-key" {
		t.Errorf("Authorization = %q，网关密钥未正确解密", call.Auth)
	}
	if call.Model != "test-tts-model" {
		t.Errorf("model = %q want test-tts-model", call.Model)
	}
	if call.Voice != "FunAudioLLM/CosyVoice2-0.5B:alex" {
		t.Errorf("voice = %q，音色未透传到上游", call.Voice)
	}
	// 130% 必须落成 speed=1.3；漏传就是"试听语速和成片不一致"。
	if call.Speed != 1.3 {
		t.Errorf("speed = %v want 1.3", call.Speed)
	}
	if call.Input != voicePreviewDefaultZH {
		t.Errorf("input = %q want 默认中文示例句", call.Input)
	}

	// 2) 超长文本：必须在调用上游之前拒绝，不能"先花钱再报错"。
	long := strings.Repeat("字", voicePreviewMaxRunes+1)
	resp = post(`{"voice":"v","text":"` + long + `"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("超长文本 status = %d want 400", resp.StatusCode)
	}
	if got := len(upstream.snapshot()); got != 1 {
		t.Errorf("超长文本仍调用了上游：累计 %d 次", got)
	}
}
