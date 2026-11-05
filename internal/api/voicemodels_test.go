package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/F31/ppts/internal/project"
)

// voice-models 是编辑器主路径上的「可选能力探测」：没配网关与网关里没有模型
// 都只是「当前没有可用模型」，必须是 200 空数组。用 503 表达配置缺失会让浏览器
// 对每个 5xx 记红色错误，一打开编辑器就刷屏，也把配置差异说成服务故障。
func TestEditorVoiceModelsDisabledReturnsEmptyList(t *testing.T) {
	projects := &fakeProjectStore{projects: []*project.Project{{
		ID: "project-1", TenantID: "tenant-1", OwnerUser: "user-1", Title: "语音模型", CurrentRevision: 1,
	}}}
	cases := []struct {
		name       string
		gatewaySet bool
		wantFlag   bool
	}{
		{"网关未配置", false, true},
		{"网关已配置但无 TTS 模型", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{DevHeaders: true}
			if tc.gatewaySet {
				opts.Gateway = newFakeGatewayStore()
			}
			server := httptest.NewServer(NewHandler(
				projects, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{},
				&fakeArtifactStore{}, testObjects(t), nil, opts,
			))
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodGet, server.URL+"/projects/project-1/voice-models", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var body struct {
				Models   []map[string]any `json:"models"`
				Disabled bool             `json:"disabled"`
				Message  string           `json:"message"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Models == nil || len(body.Models) != 0 {
				t.Fatalf("models = %#v, want empty array (not null)", body.Models)
			}
			if body.Disabled != tc.wantFlag {
				t.Fatalf("disabled = %v, want %v (message=%q)", body.Disabled, tc.wantFlag, body.Message)
			}
			if tc.wantFlag && body.Message == "" {
				t.Fatal("gateway disabled response must keep a diagnostic reason")
			}
		})
	}
}

func TestEditorVoiceModelsRequiresIdentity(t *testing.T) {
	server := httptest.NewServer(NewHandler(
		&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{},
		&fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true},
	))
	t.Cleanup(server.Close)
	resp, err := http.Get(server.URL + "/projects/project-1/voice-models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
