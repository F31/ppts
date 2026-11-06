package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/gateway"
)

// 本地语音模型（Qwen3-TTS）用 /v1/voices 返回 {"voices":[...],"languages":[...]}，
// 且不校验 API Key。音色下拉必须列出真实音色，不能把语言列表混进去，也不能因为
// 空 key 就整批跳过、退回配置里写死的单个兜底音色。
func TestFetchTTSVoicesAnonymousLocal(t *testing.T) {
	const body = `{"model":"qwen3-tts-1.7b-customvoice","voices":["aiden","dylan","eric","ono_anna","ryan","serena","sohee","uncle_fu","vivian"],"languages":["auto","chinese","english"]}`
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/voices" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cfg := &gateway.Config{BaseURL: srv.URL, Model: "qwen3-tts-1.7b-customvoice"}
	voices := fetchTTSVoices(context.Background(), cfg, "/v1/voices")
	if len(voices) != 9 {
		t.Fatalf("voices = %v, 期望 9 个真实音色", voices)
	}
	for _, bad := range []string{"auto", "chinese", "english", cfg.Model} {
		for _, v := range voices {
			if v == bad {
				t.Fatalf("音色列表混入了非音色项 %q: %v", bad, voices)
			}
		}
	}
	if gotAuth != "" {
		t.Fatalf("空 key 的本地网关不应发送 Authorization，实际 %q", gotAuth)
	}
}

func TestFetchTTSVoicesSendsKeyWhenConfigured(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"voices":["alex"]}`))
	}))
	defer srv.Close()

	cfg := &gateway.Config{BaseURL: srv.URL, APIKey: "sk-remote", Model: "m"}
	voices := fetchTTSVoices(context.Background(), cfg, "/v1/voices")
	if len(voices) != 1 || voices[0] != "alex" {
		t.Fatalf("voices = %v", voices)
	}
	if gotAuth != "Bearer sk-remote" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestParseVoiceListIgnoresLanguageCatalog(t *testing.T) {
	out := parseVoiceList([]byte(`{"voices":["vivian"],"languages":["auto","chinese"]}`), "qwen3-tts")
	if len(out) != 1 || !strings.EqualFold(out[0], "vivian") {
		t.Fatalf("parseVoiceList = %v", out)
	}
}
