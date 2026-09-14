package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type OIDCConfig struct {
	Issuer      string
	ClientID    string
	TenantClaim string
	UserClaim   string
}

type OIDCAuthenticator struct {
	verifier    *oidc.IDTokenVerifier
	tenantClaim string
	userClaim   string
}

func NewOIDCAuthenticator(ctx context.Context, cfg OIDCConfig) (*OIDCAuthenticator, error) {
	if strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.ClientID) == "" {
		return nil, errors.New("oidc: issuer and client id are required")
	}
	if cfg.TenantClaim == "" {
		cfg.TenantClaim = "tenant_id"
	}
	if cfg.UserClaim == "" {
		cfg.UserClaim = "sub"
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover provider: %w", err)
	}
	return &OIDCAuthenticator{
		verifier:    provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		tenantClaim: cfg.TenantClaim,
		userClaim:   cfg.UserClaim,
	}, nil
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return Principal{}, false, nil
	}
	parts := strings.Fields(raw)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Principal{}, false, errors.New("invalid authorization header")
	}
	token, err := a.verifier.Verify(ctx, parts[1])
	if err != nil {
		return Principal{}, false, fmt.Errorf("invalid oidc token: %w", err)
	}
	claims := map[string]any{}
	if err := token.Claims(&claims); err != nil {
		return Principal{}, false, fmt.Errorf("invalid oidc claims: %w", err)
	}
	tenantID := stringClaim(claims, a.tenantClaim)
	userID := stringClaim(claims, a.userClaim)
	if userID == "" && a.userClaim != "sub" {
		userID = token.Subject
	}
	if userID == "" {
		userID = token.Subject
	}
	if tenantID == "" || userID == "" {
		return Principal{}, false, errors.New("oidc token missing tenant or user claim")
	}
	return Principal{TenantID: tenantID, UserID: userID}, true, nil
}

func stringClaim(claims map[string]any, name string) string {
	if v, ok := claims[name].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
