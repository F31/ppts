package api

import (
	"context"
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
