//go:build pg

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/messaging"
	"github.com/F31/ppts/internal/tenant"
)

// testCipher 是固定 32 字节密钥的 AES-GCM 加密器。
func testCipher(t *testing.T) tenant.CredentialCipher {
	t.Helper()
	c, err := tenant.NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef")) // 32 bytes
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}

// TestMessageEmailConfigRoundTrip 覆盖发件箱配置的保存→读取（密码掩码）→解析（含密码）→构建发送器。
func TestMessageEmailConfigRoundTrip(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	store := messaging.NewPGStore(pool, testCipher(t))
	tenantID := "00000000-0000-0000-0000-0000000000f1"
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM message_channels WHERE tenant_id=$1`, tenantID) })

	in := messaging.EmailInput{
		Enabled: true, Host: "smtp.example.com", Port: 587, Username: "mailer",
		FromAddress: "no-reply@example.com", FromName: "PPTS", TLSMode: "starttls", Password: "s3cret-pass",
	}
	if err := store.SaveEmail(ctx, tenantID, in); err != nil {
		t.Fatalf("SaveEmail: %v", err)
	}
	cfg, err := store.GetEmail(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetEmail: %v", err)
	}
	if !cfg.Enabled || cfg.Host != "smtp.example.com" || cfg.Port != 587 || cfg.FromAddress != "no-reply@example.com" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !cfg.HasPassword || cfg.PasswordMasked == "" || strings.Contains(cfg.PasswordMasked, "s3cret-pass") {
		t.Fatalf("password should be masked: %+v", cfg)
	}
	rt, err := store.ResolveEmail(ctx, tenantID)
	if err != nil || rt.Password != "s3cret-pass" {
		t.Fatalf("resolve: %+v err=%v", rt, err)
	}
	if _, err := messaging.BuildSender(rt); err != nil {
		t.Fatalf("BuildSender: %v", err)
	}

	// 空密码更新应保留原密码。
	in.Password = ""
	in.FromName = "PPTS Renamed"
	if err := store.SaveEmail(ctx, tenantID, in); err != nil {
		t.Fatalf("SaveEmail keep password: %v", err)
	}
	rt2, err := store.ResolveEmail(ctx, tenantID)
	if err != nil || rt2.Password != "s3cret-pass" || rt2.FromName != "PPTS Renamed" {
		t.Fatalf("keep password resolve: %+v err=%v", rt2, err)
	}

	// 校验：缺 host / 非法 from。
	if err := store.SaveEmail(ctx, tenantID, messaging.EmailInput{FromAddress: "a@b.com"}); err == nil {
		t.Fatal("missing host should fail validation")
	}
	if err := store.SaveEmail(ctx, tenantID, messaging.EmailInput{Host: "h", FromAddress: "not-an-email"}); err == nil {
		t.Fatal("invalid from should fail validation")
	}
}

// TestMessagePlatformFallback 覆盖解析回退：租户未配置时用平台默认行。
func TestMessagePlatformFallback(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	store := messaging.NewPGStore(pool, testCipher(t))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_channels WHERE tenant_id IN ($1,$2)`, messaging.PlatformTenantID, "00000000-0000-0000-0000-0000000000f2")
	})
	if err := store.SaveEmail(ctx, messaging.PlatformTenantID, messaging.EmailInput{
		Enabled: true, Host: "platform-smtp", Port: 465, FromAddress: "p@example.com", TLSMode: "tls", Password: "pw",
	}); err != nil {
		t.Fatalf("save platform: %v", err)
	}
	rt, err := store.ResolveEmail(ctx, "00000000-0000-0000-0000-0000000000f2")
	if err != nil || rt.Host != "platform-smtp" {
		t.Fatalf("platform fallback: %+v err=%v", rt, err)
	}
}

// TestMessageHandlerEmailAPI 覆盖 GET/PUT 端点与运营商平台默认。
func TestMessageHandlerEmailAPI(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	const tenantID, userID = "00000000-0000-0000-0000-0000000000f3", "op-user"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_channels WHERE tenant_id IN ($1,$2)`, tenantID, messaging.PlatformTenantID)
	})
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), principalKey{}, Principal{TenantID: tenantID, UserID: userID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	mux := http.NewServeMux()
	NewMessageHandler(messaging.NewPGStore(pool, testCipher(t)), nil, nil, map[string]bool{userID: true}).Register(mux, auth)

	// PUT 保存（平台默认，因是运营商）。
	body := `{"enabled":true,"host":"smtp.x","port":587,"username":"u","from_address":"a@b.com","from_name":"N","tls_mode":"starttls","password":"pw","platform_default":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/message-channels/email", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}

	// GET 返回 email + platform_email（运营商）。
	req = httptest.NewRequest(http.MethodGet, "/api/message-channels", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["is_operator"] != true {
		t.Fatalf("is_operator = %v", out["is_operator"])
	}
	if _, ok := out["platform_email"]; !ok {
		t.Fatalf("platform_email missing: %v", out)
	}
	_ = time.Now
}
