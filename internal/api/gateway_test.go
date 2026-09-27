package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/membership"
)

type fakeGatewayStore struct {
	rows map[string]*gateway.Gateway
}

func newFakeGatewayStore() *fakeGatewayStore {
	return &fakeGatewayStore{rows: map[string]*gateway.Gateway{}}
}

func gwKey(tenant, name, kind string) string { return tenant + "|" + name + "|" + kind }

func (f *fakeGatewayStore) List(_ context.Context, tenantID string, kind gateway.Kind) ([]*gateway.Gateway, error) {
	var out []*gateway.Gateway
	for _, g := range f.rows {
		if g.TenantID == tenantID && g.Kind == kind {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeGatewayStore) Get(_ context.Context, tenantID, name string, kind gateway.Kind) (*gateway.Gateway, error) {
	g, ok := f.rows[gwKey(tenantID, name, string(kind))]
	if !ok {
		return nil, gateway.ErrNotFound
	}
	return g, nil
}

func (f *fakeGatewayStore) ResolveNamed(_ context.Context, tenantID, name string, kind gateway.Kind) (*gateway.Config, error) {
	g, err := f.Get(context.Background(), tenantID, name, kind)
	if err != nil {
		return nil, err
	}
	return &gateway.Config{Name: g.Name, Kind: g.Kind, BaseURL: g.BaseURL, APIKey: "secret", Model: g.Model}, nil
}

func (f *fakeGatewayStore) Create(_ context.Context, g *gateway.Gateway, _ string) error {
	g.Version = 1
	f.rows[gwKey(g.TenantID, g.Name, string(g.Kind))] = g
	return nil
}

func (f *fakeGatewayStore) Update(_ context.Context, g *gateway.Gateway, _ string) error {
	key := gwKey(g.TenantID, g.Name, string(g.Kind))
	if _, ok := f.rows[key]; !ok {
		return gateway.ErrNotFound
	}
	g.Version++
	f.rows[key] = g
	return nil
}

func (f *fakeGatewayStore) ChangeKind(_ context.Context, g *gateway.Gateway, oldKind gateway.Kind, _ string) error {
	oldKey := gwKey(g.TenantID, g.Name, string(oldKind))
	if _, ok := f.rows[oldKey]; !ok {
		return gateway.ErrNotFound
	}
	newKey := gwKey(g.TenantID, g.Name, string(g.Kind))
	if oldKey != newKey {
		if _, ok := f.rows[newKey]; ok {
			return gateway.ErrExists
		}
	}
	g.Version++
	delete(f.rows, oldKey)
	f.rows[newKey] = g
	return nil
}

func (f *fakeGatewayStore) Delete(_ context.Context, tenantID, name string, kind gateway.Kind) error {
	if _, ok := f.rows[gwKey(tenantID, name, string(kind))]; !ok {
		return gateway.ErrNotFound
	}
	delete(f.rows, gwKey(tenantID, name, string(kind)))
	return nil
}

func (f *fakeGatewayStore) SetDefault(_ context.Context, tenantID, name string, kind gateway.Kind) error {
	for _, g := range f.rows {
		if g.TenantID == tenantID && g.Kind == kind {
			g.IsDefault = false
		}
	}
	g, ok := f.rows[gwKey(tenantID, name, string(kind))]
	if !ok {
		return gateway.ErrNotFound
	}
	g.IsDefault = true
	return nil
}

func (f *fakeGatewayStore) Resolve(_ context.Context, tenantID string, kind gateway.Kind) (*gateway.Config, error) {
	if _, err := f.Get(context.Background(), tenantID, "", kind); err != nil {
		return nil, gateway.ErrNotFound
	}
	return &gateway.Config{}, nil
}

func (f *fakeGatewayStore) Invalidate(_ string, _ gateway.Kind) {}

var _ gateway.StoreResolver = (*fakeGatewayStore)(nil)

func gwHandler(t *testing.T, store *fakeGatewayStore, role membership.Role) http.Handler {
	t.Helper()
	// DevHeaders 必须显式开启：这些测例靠 tenantHeader/userHeader 充当已认证身份。
	// allowDevHeaders 已不再因 Auth==nil 自动放行——生产漏配 secret 不能再等同于开发模式。
	return NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil,
		Options{Members: &fakeRoleReader{role: role}, Gateway: store, DevHeaders: true})
}

func gwDo(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenantHeader, "00000000-0000-0000-0000-000000000001")
	req.Header.Set(userHeader, "user-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGatewayCRUDRequiresAdmin(t *testing.T) {
	store := newFakeGatewayStore()

	// 非 admin 被拒绝。
	rec := gwDo(t, gwHandler(t, store, membership.RoleViewer), "GET", "/api/model-gateways", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer list code = %d want 403", rec.Code)
	}

	rec = gwDo(t, gwHandler(t, store, membership.RoleAdmin), "POST", "/api/model-gateways", `{"kind":"tts","name":"sil","baseUrl":"https://api.siliconflow.cn","apiKey":"sk-1","model":"cosy","isDefault":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code = %d body=%s", rec.Code, rec.Body.String())
	}
	gws, err := store.List(context.Background(), "00000000-0000-0000-0000-000000000001", gateway.KindTTS)
	if err != nil || len(gws) != 1 {
		t.Fatalf("list after create = %d err=%v", len(gws), err)
	}
	if !gws[0].IsDefault {
		t.Fatalf("expected default gateway")
	}
}

func TestGatewayPlatformScopeRequiresOperator(t *testing.T) {
	t.Setenv("PPTS_OPERATOR_USER_IDS", "another-user")
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/model-gateways?scope=platform"},
		{"POST", "/api/model-gateways?scope=platform"},
		{"PUT", "/api/model-gateways/sil?scope=platform"},
		{"DELETE", "/api/model-gateways/sil?scope=platform&kind=tts"},
		{"POST", "/api/model-gateways/sil/set-default?scope=platform&kind=tts"},
		{"POST", "/api/model-gateways/sil/test?scope=platform&kind=tts"},
	} {
		rec := gwDo(t, h, route.method, route.path, `{}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: got %d, want 403", route.method, route.path, rec.Code)
		}
	}
	if rec := gwDo(t, h, "GET", "/api/model-gateways?scope=other", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope: got %d", rec.Code)
	}
	// 即使身份落在保留的平台租户，也不能通过省略 scope 绕过 operator 权限。
	req := httptest.NewRequest("GET", "/api/model-gateways", nil)
	req.Header.Set(tenantHeader, gateway.PlatformTenantID)
	req.Header.Set(userHeader, "user-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("implicit platform scope: got %d", rec.Code)
	}
}

func TestGatewayPlatformScopeIsolation(t *testing.T) {
	t.Setenv("PPTS_OPERATOR_USER_IDS", "user-1")
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)
	body := `{"kind":"tts","name":"sil","baseUrl":"https://api.siliconflow.cn","apiKey":"test-key","model":"cosy","isDefault":true}`
	for _, suffix := range []string{"", "?scope=platform"} {
		rec := gwDo(t, h, "POST", "/api/model-gateways"+suffix, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", suffix, rec.Code, rec.Body.String())
		}
	}
	for _, scope := range []struct{ query, tenant string }{
		{"", "00000000-0000-0000-0000-000000000001"},
		{"?scope=platform", gateway.PlatformTenantID},
	} {
		rec := gwDo(t, h, "GET", "/api/model-gateways"+scope.query, "")
		var resp struct {
			Gateways []*gateway.Gateway `json:"gateways"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || len(resp.Gateways) != 1 || resp.Gateways[0].TenantID != scope.tenant {
			t.Fatalf("list scope %q leaked other scope: %s", scope.query, rec.Body.String())
		}
	}
	// 平台操作不依赖管理员在自己的租户是否为 admin。
	h = gwHandler(t, store, membership.RoleViewer)
	for _, route := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/model-gateways?scope=platform", "", http.StatusOK},
		{"PUT", "/api/model-gateways/sil?scope=platform", `{"kind":"tts","version":1,"model":"updated"}`, http.StatusOK},
		{"POST", "/api/model-gateways/sil/set-default?scope=platform&kind=tts", "", http.StatusNoContent},
		{"POST", "/api/model-gateways/sil/test?scope=platform&kind=tts", "", http.StatusOK},
	} {
		rec := gwDo(t, h, route.method, route.path, route.body)
		if rec.Code != route.status {
			t.Fatalf("%s %s: %d %s", route.method, route.path, rec.Code, rec.Body.String())
		}
	}
	platform, err := store.Get(context.Background(), gateway.PlatformTenantID, "sil", gateway.KindTTS)
	if err != nil || platform.Model != "updated" || !platform.IsDefault {
		t.Fatalf("platform update not applied: %+v %v", platform, err)
	}
	if rec := gwDo(t, h, "DELETE", "/api/model-gateways/sil?scope=platform&kind=tts", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete platform: %d", rec.Code)
	}
	tenant, err := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "sil", gateway.KindTTS)
	if err != nil || tenant.Model != "cosy" || !tenant.IsDefault {
		t.Fatalf("tenant gateway changed by platform operations: %+v %v", tenant, err)
	}
	if rec := gwDo(t, h, "GET", "/api/model-gateways", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant viewer should still be forbidden: %d", rec.Code)
	}
}

func TestGatewayListAndTest(t *testing.T) {
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)

	gwDo(t, h, "POST", "/api/model-gateways", `{"kind":"llm","name":"q","baseUrl":"https://api.siliconflow.cn","apiKey":"sk-2","model":"qwen","visionModel":"qv"}`)

	rec := gwDo(t, h, "GET", "/api/model-gateways?kind=llm", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list code = %d", rec.Code)
	}
	var resp struct {
		Gateways []*gateway.Gateway `json:"gateways"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Gateways) != 1 {
		t.Fatalf("gateways = %d", len(resp.Gateways))
	}

	// 探活：fake store 返回配置，Probe 会真实请求 base_url（无网络端点会失败）——只验证结构可达。
	rec = gwDo(t, h, "POST", "/api/model-gateways/q/test?kind=llm", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("test code = %d body=%s", rec.Code, rec.Body.String())
	}
	var tr struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("decode test: %v", err)
	}
}

func TestGatewayDeleteAndNotFound(t *testing.T) {
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)
	gwDo(t, h, "POST", "/api/model-gateways", `{"kind":"tts","name":"x","baseUrl":"https://x","apiKey":"k","model":"m"}`)

	rec := gwDo(t, h, "DELETE", "/api/model-gateways/x?kind=tts", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete code = %d", rec.Code)
	}
	rec = gwDo(t, h, "DELETE", "/api/model-gateways/x?kind=tts", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete again code = %d want 404", rec.Code)
	}
}

func TestGatewayUpdateMerges(t *testing.T) {
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)
	gwDo(t, h, "POST", "/api/model-gateways", `{"kind":"llm","name":"q","baseUrl":"https://a","apiKey":"k","model":"m1","visionModel":"v1"}`)
	g, _ := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "q", gateway.KindLLM)

	// 只改 model，保留 baseUrl。
	rec := gwDo(t, h, "PUT", "/api/model-gateways/q", `{"kind":"llm","version":`+strconv.Itoa(g.Version)+`,"model":"m2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update code = %d body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "q", gateway.KindLLM)
	if updated.Model != "m2" || updated.BaseURL != "https://a" {
		t.Fatalf("merge = %+v", updated)
	}
}

func TestGatewayUpdateSwitchesKind(t *testing.T) {
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)
	gwDo(t, h, "POST", "/api/model-gateways", `{"kind":"tts","name":"svc","baseUrl":"https://a","apiKey":"k","model":"m","voice":"v1"}`)
	g, _ := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "svc", gateway.KindTTS)

	rec := gwDo(t, h, "PUT", "/api/model-gateways/svc", `{"kind":"llm","originalKind":"tts","version":`+strconv.Itoa(g.Version)+`,"model":"m2","visionModel":"v2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("switch kind code = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "svc", gateway.KindTTS); !errors.Is(err, gateway.ErrNotFound) {
		t.Fatalf("old kind row should be gone, err=%v", err)
	}
	llm, err := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "svc", gateway.KindLLM)
	if err != nil || llm.Model != "m2" {
		t.Fatalf("llm after switch = %+v err=%v", llm, err)
	}
}

func TestGatewayUpdateSwitchKindConflict(t *testing.T) {
	store := newFakeGatewayStore()
	h := gwHandler(t, store, membership.RoleAdmin)
	gwDo(t, h, "POST", "/api/model-gateways", `{"kind":"tts","name":"dup","baseUrl":"https://a","apiKey":"k","model":"m"}`)
	gwDo(t, h, "POST", "/api/model-gateways", `{"kind":"llm","name":"dup","baseUrl":"https://b","apiKey":"k","model":"m"}`)
	g, _ := store.Get(context.Background(), "00000000-0000-0000-0000-000000000001", "dup", gateway.KindTTS)

	rec := gwDo(t, h, "PUT", "/api/model-gateways/dup", `{"kind":"llm","originalKind":"tts","version":`+strconv.Itoa(g.Version)+`,"model":"m2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict code = %d want 409 body=%s", rec.Code, rec.Body.String())
	}
}
