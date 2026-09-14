package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	tenantHeader = "X-PPTS-Tenant-ID"
	userHeader   = "X-PPTS-User-ID"
)

type principalKey struct{}

// Principal is the identity established by the trusted authentication proxy.
// G3 replaces these development headers with verified OIDC claims.
type Principal struct {
	TenantID string
	UserID   string
}

// TenantStatusChecker 检查租户生命周期状态（G3-4）。
type TenantStatusChecker interface {
	TenantActive(ctx context.Context, tenantID string) (bool, error)
}

// Authenticator verifies a production identity source (for example OIDC bearer tokens).
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Principal, bool, error)
}

type AuthOptions struct {
	TenantStatus    TenantStatusChecker
	Authenticator   Authenticator
	AllowDevHeaders bool
}

// PrincipalFromContext returns the authenticated tenant and user.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// AuthMiddlewareWithOptions authenticates OIDC bearer tokens when configured and
// only falls back to development headers when explicitly allowed.
func AuthMiddlewareWithOptions(next http.Handler, opts AuthOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok, err := authenticateRequest(r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if !ok {
			http.Error(w, "missing authenticated tenant or user", http.StatusUnauthorized)
			return
		}
		if opts.TenantStatus != nil {
			active, err := opts.TenantStatus.TenantActive(r.Context(), principal.TenantID)
			if err != nil || !active {
				http.Error(w, "tenant is not active", http.StatusForbidden)
				return
			}
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authenticateRequest(r *http.Request, opts AuthOptions) (Principal, bool, error) {
	if opts.Authenticator != nil {
		principal, ok, err := opts.Authenticator.Authenticate(r.Context(), r)
		if err != nil || ok {
			return principal, ok, err
		}
	}
	if !opts.AllowDevHeaders {
		return Principal{}, false, nil
	}
	tenantID := strings.TrimSpace(r.Header.Get(tenantHeader))
	userID := strings.TrimSpace(r.Header.Get(userHeader))
	if tenantID == "" && userID == "" {
		return Principal{}, false, nil
	}
	if tenantID == "" || userID == "" {
		return Principal{}, false, errors.New("missing authenticated tenant or user")
	}
	return Principal{TenantID: tenantID, UserID: userID}, true, nil
}
