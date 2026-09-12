package api

import (
	"context"
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

// PrincipalFromContext returns the authenticated tenant and user.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// AuthMiddleware rejects requests without the trusted identity headers and
// injects the resulting principal into the request context.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := strings.TrimSpace(r.Header.Get(tenantHeader))
		userID := strings.TrimSpace(r.Header.Get(userHeader))
		if tenantID == "" || userID == "" {
			http.Error(w, "missing authenticated tenant or user", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{
			TenantID: tenantID,
			UserID:   userID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
