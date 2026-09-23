package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassifyAccount(t *testing.T) {
	cases := []struct {
		in    string
		kind  accountKind
		value string
		ok    bool
	}{
		{"Alice@Example.com", accountKindEmail, "alice@example.com", true},
		{"  bob@x.io ", accountKindEmail, "bob@x.io", true},
		{"138 0013 8000", accountKindPhone, "13800138000", true},
		{"+86-138-0013-8000", accountKindPhone, "+8613800138000", true},
		{"(415) 555-2671", accountKindPhone, "4155552671", true},
		{"", accountKindEmail, "", false},
		{"no-at-no-digits", accountKindEmail, "", false},
		{"a@b", accountKindEmail, "", false},
		{"1234", accountKindPhone, "", false},
		{"+", accountKindPhone, "", false},
	}
	for _, c := range cases {
		got, err := classifyAccount(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("classifyAccount(%q) err=%v want ok=%v", c.in, err, c.ok)
		}
		if err != nil {
			continue
		}
		if got.Kind != c.kind || got.String() != c.value {
			t.Errorf("classifyAccount(%q) = kind:%v value:%q want kind:%v value:%q", c.in, got.Kind, got.String(), c.kind, c.value)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	good := []string{"Str0ng-Passw0rd-9", "CorrectHorse9Battery", "p@ssW0rdLongEnough"}
	for _, p := range good {
		if err := validatePassword(p, "alice"); err != nil {
			t.Errorf("validatePassword(%q) = %v want nil", p, err)
		}
	}
	bad := map[string]string{
		"short1A!":      "too short",
		"alllowercase":  "only one class",
		"password123":   "common",
		"PASSWORD123":   "common (case-insensitive)",
		"Alice-Secret9": "contains account",
	}
	for p, why := range bad {
		if err := validatePassword(p, "alice"); err == nil {
			t.Errorf("validatePassword(%q) = nil want error (%s)", p, why)
		}
	}
}

func TestFixedWindowLimiter(t *testing.T) {
	l := newFixedWindowLimiter(time.Minute, 3)
	for i := 0; i < 3; i++ {
		if !l.allow("ip1") {
			t.Fatalf("allow #%d should pass", i+1)
		}
	}
	if l.allow("ip1") {
		t.Fatal("4th allow should be rejected")
	}
	if !l.allow("ip2") {
		t.Fatal("different key should not be limited")
	}
}

func TestFailureGuard(t *testing.T) {
	g := newFailureGuard(3, time.Minute)
	for i := 0; i < 2; i++ {
		g.fail("acct")
	}
	if g.locked("acct") {
		t.Fatal("should not lock before threshold")
	}
	g.fail("acct")
	if !g.locked("acct") {
		t.Fatal("should lock at threshold")
	}
	g.reset("acct")
	if g.locked("acct") {
		t.Fatal("reset should clear lock")
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), SecurityHeadersConfig{
		Enabled: true,
		CSP:     buildCSP([]string{"https://idp.example.com"}),
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing X-Frame-Options")
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "https://idp.example.com") {
		t.Fatalf("unexpected CSP: %q", csp)
	}
}
