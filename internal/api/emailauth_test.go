package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func makeToken(t *testing.T, secret string, claims jwtClaims) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	input := b64urlEncode(header) + "." + b64urlEncode(payload)
	return input + "." + signHS256(input, []byte(secret))
}

func TestIssueVerifyTokenRoundtrip(t *testing.T) {
	token, err := issueToken("tenant-1", "user-1", "secret")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, ok := verifyToken(token, "secret")
	if !ok {
		t.Fatal("expected valid token")
	}
	if claims.Sub != "user-1" || claims.Tid != "tenant-1" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if claims.Iss != jwtIssuer || claims.Aud != jwtAudience {
		t.Fatalf("iss/aud mismatch: %+v", claims)
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	token, _ := issueToken("tenant-1", "user-1", "secret")
	if _, ok := verifyToken(token, "other"); ok {
		t.Fatal("token must not verify under wrong secret")
	}
}

func TestVerifyTokenTampered(t *testing.T) {
	token, _ := issueToken("tenant-1", "user-1", "secret")
	// 篡改签名段。
	parts := strings.Split(token, ".")
	parts[2] = parts[2] + "x"
	if _, ok := verifyToken(strings.Join(parts, "."), "secret"); ok {
		t.Fatal("tampered token must not verify")
	}
}

func TestVerifyTokenAlgConfusion(t *testing.T) {
	// 构造 alg=none 的令牌，必须被拒绝。
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(jwtClaims{Sub: "u", Tid: "t", Iss: jwtIssuer, Aud: jwtAudience, Exp: time.Now().Add(time.Hour).Unix()})
	input := b64urlEncode(header) + "." + b64urlEncode(payload)
	if _, ok := verifyToken(input+".", "secret"); ok {
		t.Fatal("alg=none token must be rejected")
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	token := makeToken(t, "secret", jwtClaims{
		Sub: "u", Tid: "t", Iss: jwtIssuer, Aud: jwtAudience,
		Exp: time.Now().Add(-time.Hour).Unix(), Iat: time.Now().Add(-2 * time.Hour).Unix(),
	})
	if _, ok := verifyToken(token, "secret"); ok {
		t.Fatal("expired token must not verify")
	}
}

func TestHashPasswordVerify(t *testing.T) {
	h, err := hashPassword("password123", "pepper")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !verifyPassword(h, "password123", "pepper") {
		t.Fatal("correct password must verify")
	}
	if verifyPassword(h, "wrong", "pepper") {
		t.Fatal("wrong password must not verify")
	}
	if verifyPassword(h, "password123", "wrong-pepper") {
		t.Fatal("wrong pepper must not verify")
	}
}

func TestValidateOrgName(t *testing.T) {
	cases := map[string]bool{
		"北京分公司":                 true,
		"Acme Inc.":             true,
		"R&D_Team-1":            true,
		"ab":                    true,
		"a":                     false, // 少于 2 个字符
		"":                      false,
		" ":                     false, // 纯空格
		strings.Repeat("x", 40): true,
		strings.Repeat("x", 41): false, // 超过 40 个字符
		"bad😀":                  false, // emoji 不允许
		"a<b":                   false, // 尖括号不允许
		"公司/部门":                 false, // 斜杠不允许
	}
	for name, want := range cases {
		err := validateOrgName(strings.TrimSpace(name))
		if (err == nil) != want {
			t.Errorf("validateOrgName(%q) err=%v want ok=%v", name, err, want)
		}
	}
}

func TestDefaultPersonalTenantName(t *testing.T) {
	if got, want := defaultPersonalTenantName("zhangsan@xx.com"), "zhangsan"+personalTenantSuffix; got != want {
		t.Errorf("email account => %q want %q", got, want)
	}
	// 手机号（无 @）：取整个账号。
	if got, want := defaultPersonalTenantName("13800138000"), "13800138000"+personalTenantSuffix; got != want {
		t.Errorf("phone account => %q want %q", got, want)
	}
}

func TestValidAccount(t *testing.T) {
	cases := map[string]bool{
		"a@example.com":  true,
		"x@y.io":         true,
		"UPPER@X.COM":    true,
		"13800138000":    true,
		"+8613800138000": true,
		"+14155552671":   true,
		"":               false,
		"no-at":          false,
		"a@b":            false,
		"a b@x.com":      false,
		"a..b@x.com":     false,
		"1234":           false, // 少于 5 位
		"1380013800a":    false, // 含字母
		"+":              false,
	}
	for account, want := range cases {
		if got := validAccount(account); got != want {
			t.Errorf("validAccount(%q)=%v want %v", account, got, want)
		}
	}
}

func TestCombinedAuthenticatorJWTOnly(t *testing.T) {
	auth := NewCombinedAuthenticator(nil, NewJWTAuthenticator("secret"))
	token, _ := issueToken("tenant-9", "user-9", "secret")

	req := httptest.NewRequest(http.MethodPost, "/auth/email-login", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	p, ok, err := auth.Authenticate(context.Background(), req)
	if err != nil || !ok {
		t.Fatalf("expected principal, ok=%v err=%v", ok, err)
	}
	if p.TenantID != "tenant-9" || p.UserID != "user-9" {
		t.Fatalf("principal mismatch: %+v", p)
	}

	// 非本系统令牌：回退路径应返回 (false,nil)，不报错（交由 OIDC 判定）。
	bad := httptest.NewRequest(http.MethodPost, "/x", nil)
	bad.Header.Set("Authorization", "Bearer not-our-token")
	if _, ok, err := auth.Authenticate(context.Background(), bad); err != nil || ok {
		t.Fatalf("expected fallback (false,nil), got ok=%v err=%v", ok, err)
	}
}
