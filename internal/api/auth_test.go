package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeAuthenticator struct {
	principal Principal
	ok        bool
	err       error
}

func (f fakeAuthenticator) Authenticate(context.Context, *http.Request) (Principal, bool, error) {
	return f.principal, f.ok, f.err
}

func principalHandler(t *testing.T, want Principal) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatalf("principal missing")
		}
		if got != want {
			t.Fatalf("principal = %+v want %+v", got, want)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestAuthMiddlewareUsesAuthenticatorPrincipal(t *testing.T) {
	want := Principal{TenantID: "tenant-1", UserID: "user-1"}
	h := AuthMiddlewareWithOptions(principalHandler(t, want), AuthOptions{
		Authenticator: fakeAuthenticator{principal: want, ok: true},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/rpc", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAuthMiddlewareDisablesDevHeadersWhenAuthenticatorConfigured(t *testing.T) {
	h := AuthMiddlewareWithOptions(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("handler should not run")
	}), AuthOptions{Authenticator: fakeAuthenticator{ok: false}})
	req := httptest.NewRequest(http.MethodPost, "/rpc", nil)
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddlewareAllowsExplicitDevHeaderFallback(t *testing.T) {
	want := Principal{TenantID: "tenant-1", UserID: "user-1"}
	h := AuthMiddlewareWithOptions(principalHandler(t, want), AuthOptions{
		Authenticator:   fakeAuthenticator{ok: false},
		AllowDevHeaders: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/rpc", nil)
	req.Header.Set(tenantHeader, want.TenantID)
	req.Header.Set(userHeader, want.UserID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

// 单租户本地模式：不解析任何凭证，直接注入固定身份，且忽略 dev 头与 Authenticator。
func TestAuthMiddlewareLocalPrincipal(t *testing.T) {
	want := Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "local-user"}
	h := AuthMiddlewareWithOptions(principalHandler(t, want), AuthOptions{
		LocalPrincipal: &want,
		// 即便配置了 Authenticator，本地模式也不应调用它（用会失败的桩验证）。
		Authenticator: fakeAuthenticator{ok: false},
	})
	req := httptest.NewRequest(http.MethodGet, "/ppts.v1.ProjectService/List", nil)
	// 携带冲突的 dev 头，应被忽略。
	req.Header.Set(tenantHeader, "someone-else")
	req.Header.Set(userHeader, "someone-else")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

// 本地模式下租户被暂停应返回 403（保留生命周期检查）。
type fakeStatus struct{ active bool }

func (f fakeStatus) TenantActive(context.Context, string) (bool, error) { return f.active, nil }

func TestAuthMiddlewareLocalPrincipalRespectsTenantStatus(t *testing.T) {
	local := Principal{TenantID: "t", UserID: "u"}
	h := AuthMiddlewareWithOptions(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run when tenant suspended")
	}), AuthOptions{LocalPrincipal: &local, TenantStatus: fakeStatus{active: false}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d want 403", rec.Code)
	}
}

// 单租户本地模式端到端：/auth/config 返回 local=true，且受保护路由无凭证可访问。
func TestLocalModeAuthConfigAndAccess(t *testing.T) {
	local := Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "local-user"}
	h := NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{},
		&fakeArtifactStore{}, testObjects(t), nil,
		Options{LocalPrincipal: &local})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/auth/config")
	if err != nil {
		t.Fatalf("auth config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth config status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("auth config json: %v body=%s", err, body)
	}
	if cfg["local"] != true {
		t.Fatalf("expected local=true, got %v", cfg)
	}
	if cfg["tenant_id"] != "00000000-0000-0000-0000-000000000001" || cfg["user_id"] != "local-user" {
		t.Fatalf("unexpected identity in config: %v", cfg)
	}
	if cfg["email_password"] != false {
		t.Fatalf("local mode should have email_password=false, got %v", cfg["email_password"])
	}

	// 受保护路由无凭证访问（本地模式注入固定 Principal）。
	orgResp, err := http.Get(srv.URL + "/projects/organization")
	if err != nil {
		t.Fatalf("organization: %v", err)
	}
	defer orgResp.Body.Close()
	if orgResp.StatusCode != http.StatusOK {
		t.Fatalf("organization should be 200 in local mode, got %d", orgResp.StatusCode)
	}
}
