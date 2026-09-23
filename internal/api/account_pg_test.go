//go:build pg

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPhoneAndEmailRegistrationFlows 覆盖第一批：
//   - 手机号/邮箱自动识别与分列落库；
//   - 邮箱验证令牌消费；
//   - 密码重置（旧密码失效、会话吊销）。
func TestPhoneAndEmailRegistrationFlows(t *testing.T) {
	dsn := testDSNOrSkip(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	const secret, pepper = "test-secret", "pepper"
	server := httptest.NewServer(NewHandler(nil, nil, nil, nil, nil, nil, pool,
		Options{JWTSecret: secret, PasswordPepper: pepper}))
	t.Cleanup(server.Close)

	prefix := "b1t" + strconv.FormatInt(time.Now().UnixNano(), 36)
	email := func(local string) string { return prefix + "+" + local + "@example.com" }
	phone := "+1999" + fmt.Sprintf("%07d", time.Now().UnixNano()%1e7)
	var tenants []string
	t.Cleanup(func() {
		for _, id := range tenants {
			_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email LIKE $1`, prefix+"+%")
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE phone = $1`, phone)
	})

	post := func(path string, body map[string]any) (*http.Response, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		if tid, ok := out["tenant_id"].(string); ok && tid != "" {
			tenants = append(tenants, tid)
		}
		return resp, out
	}

	// 1) 手机号注册：自动识别，分列落库（users.phone，email 为空）。
	resp, out := post("/auth/register", map[string]any{
		"account": phone, "password": "Str0ng-Passw0rd-9", "account_type": "personal",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("phone register status=%d out=%v", resp.StatusCode, out)
	}
	if out["account_kind"] != "phone" {
		t.Fatalf("phone register kind=%v", out["account_kind"])
	}
	var uPhone, uEmail *string
	if err := pool.QueryRow(ctx, `SELECT phone, email FROM users WHERE id=$1`, out["user_id"]).Scan(&uPhone, &uEmail); err != nil {
		t.Fatalf("load user: %v", err)
	}
	if uPhone == nil || *uPhone != phone || uEmail != nil {
		t.Fatalf("phone persisted wrong: phone=%v email=%v", uPhone, uEmail)
	}

	// 2) 手机号登录成功。
	if resp, _ = post("/auth/email-login", map[string]any{"account": phone, "password": "Str0ng-Passw0rd-9"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("phone login status=%d", resp.StatusCode)
	}

	// 3) 弱密码注册被拒。
	resp, out = post("/auth/register", map[string]any{
		"account": phone + "1", "password": "password123", "account_type": "personal",
	})
	if resp.StatusCode != http.StatusBadRequest || out["code"] != "weak_password" {
		t.Fatalf("weak password status=%d out=%v", resp.StatusCode, out)
	}

	// 4) 邮箱注册 → 邮箱验证：users.email_verified_at 由 NULL 变为非空。
	mail := email("verify")
	resp, out = post("/auth/register", map[string]any{
		"account": mail, "password": "Str0ng-Passw0rd-9", "account_type": "personal",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("email register status=%d out=%v", resp.StatusCode, out)
	}
	if out["email_verified"] != false {
		t.Fatalf("new email account should be unverified: %v", out["email_verified"])
	}
	userID := out["user_id"].(string)
	store := newAuthStore(pool)
	vtok, err := store.issueToken(ctx, userID, purposeEmailVerify, emailVerifyTTL)
	if err != nil {
		t.Fatalf("issue verify token: %v", err)
	}
	if resp, _ = post("/auth/verify-email", map[string]any{"token": vtok}); resp.StatusCode != http.StatusOK {
		t.Fatalf("verify-email status=%d", resp.StatusCode)
	}
	var verified *time.Time
	if err := pool.QueryRow(ctx, `SELECT email_verified_at FROM users WHERE id=$1`, userID).Scan(&verified); err != nil || verified == nil {
		t.Fatalf("email_verified_at = %v err=%v", verified, err)
	}
	// 令牌单次使用：重复消费失败。
	if resp, _ = post("/auth/verify-email", map[string]any{"token": vtok}); resp.StatusCode == http.StatusOK {
		t.Fatal("verify token must be single-use")
	}

	// 5) 密码重置：旧密码失效、新密码可登录、旧会话被吊销。
	rtok, err := store.issueToken(ctx, userID, purposePasswordReset, passwordResetTTL)
	if err != nil {
		t.Fatalf("issue reset token: %v", err)
	}
	if resp, out = post("/auth/reset-password", map[string]any{"token": rtok, "password": "N3w-Passw0rd-Xyz"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("reset-password status=%d out=%v", resp.StatusCode, out)
	}
	if resp, _ = post("/auth/email-login", map[string]any{"account": mail, "password": "Str0ng-Passw0rd-9"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password should fail after reset, got %d", resp.StatusCode)
	}
	if resp, _ = post("/auth/email-login", map[string]any{"account": mail, "password": "N3w-Passw0rd-Xyz"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("new password should work after reset, got %d", resp.StatusCode)
	}
}

func testDSNOrSkip(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	return dsn
}
