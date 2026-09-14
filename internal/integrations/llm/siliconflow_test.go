package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSiliconFlowRewriteSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("bad request: path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "model-1" || !strings.Contains(contentString(req.Messages[1].Content), "原文") {
			t.Fatalf("request = %+v", req)
		}
		_, _ = w.Write([]byte(`{"id":"req-1","model":"model-1","choices":[{"message":{"role":"assistant","content":"润色后的文本。"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	}))
	defer server.Close()
	provider, err := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: server.URL, APIKey: "test-key", Model: "model-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Rewrite(context.Background(), RewriteRequest{SourceText: "原始文本。", Language: "zh-CN"})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if got.Text != "润色后的文本。" || got.ProviderRequestID != "req-1" || got.PromptTokens != 10 || got.CompletionTokens != 3 {
		t.Fatalf("result = %+v", got)
	}
}

func TestSiliconFlowExtractVisualSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "vision-1" {
			t.Fatalf("model = %q", req.Model)
		}
		parts, ok := req.Messages[1].Content.([]any)
		if !ok || len(parts) != 2 {
			t.Fatalf("content parts = %#v", req.Messages[1].Content)
		}
		image, ok := parts[1].(map[string]any)["image_url"].(map[string]any)
		if !ok || !strings.HasPrefix(image["url"].(string), "data:image/png;base64,") {
			t.Fatalf("image part = %#v", parts[1])
		}
		_, _ = w.Write([]byte(`{"id":"req-2","model":"vision-1","choices":[{"message":{"role":"assistant","content":"[{\"kind\":\"visual_text\",\"raw\":\"PCIe 5.0\",\"confidence\":0.72}]"}}]}`))
	}))
	defer server.Close()
	provider, err := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: server.URL, APIKey: "test-key", VisionModel: "vision-1"})
	if err != nil {
		t.Fatal(err)
	}
	anchors, err := provider.ExtractVisual(context.Background(), VisualExtractRequest{ImagePNG: []byte("png")})
	if err != nil {
		t.Fatalf("ExtractVisual: %v", err)
	}
	if len(anchors) != 1 || anchors[0].Raw != "PCIe 5.0" || anchors[0].Confidence != 0.72 {
		t.Fatalf("anchors = %+v", anchors)
	}
}

func TestSiliconFlowRewriteRetryable429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer server.Close()
	provider, err := NewSiliconFlowProvider(SiliconFlowConfig{BaseURL: server.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Rewrite(context.Background(), RewriteRequest{SourceText: "文本"})
	retry, ok := err.(*RetryableError)
	if !ok || retry.RetryAfter <= 0 {
		t.Fatalf("retry = %#v err=%v", retry, err)
	}
}

func TestSiliconFlowRewriteRequiresAPIKey(t *testing.T) {
	if _, err := NewSiliconFlowProvider(SiliconFlowConfig{}); err == nil {
		t.Fatalf("expected missing api key error")
	}
}

func TestSiliconFlowLLMLiveSmoke(t *testing.T) {
	key := os.Getenv("PPTS_LLM_API_KEY")
	if key == "" {
		t.Skip("PPTS_LLM_API_KEY not set")
	}
	provider, err := NewSiliconFlowProvider(SiliconFlowConfig{
		BaseURL: os.Getenv("PPTS_LLM_BASE_URL"),
		APIKey:  key,
		Model:   os.Getenv("PPTS_LLM_MODEL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Rewrite(context.Background(), RewriteRequest{
		SourceText: "本产品性能卓越良好面向数据中心。",
		Language:   "zh-CN",
	})
	if err != nil {
		t.Fatalf("Rewrite live: %v", err)
	}
	if strings.TrimSpace(got.Text) == "" {
		t.Fatalf("empty live text: %+v", got)
	}
	t.Logf("model=%s prompt=%d completion=%d text=%s", got.Model, got.PromptTokens, got.CompletionTokens, got.Text)
}
