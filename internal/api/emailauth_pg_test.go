//go:build pg

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRegisterAccountTypes 覆盖 /auth/register 的账号类型分叉（S2）：
// 个人成功、组织成功、组织缺名/非法字符/超长报错、个人携带 org_name 被忽略、缺 account_type 报错。
// 需要 PG（PPTS_TEST_DATABASE），未设置时跳过。
func TestRegisterAccountTypes(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	// 关闭每 IP 每日注册上限，保证用例内多次注册不受限流影响（限流另有专门用例）。
	t.Setenv("PPTS_REGISTER_DAILY_PER_IP", "0")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	server := httptest.NewServer(NewHandler(nil, nil, nil, nil, nil, nil, pool,
		Options{DevHeaders: true, JWTSecret: "test-secret", PasswordPepper: "pepper"}))
	t.Cleanup(server.Close)

	// 唯一小写前缀：账号会被后端 lower(trim)，前缀必须全小写以保证断言一致。
	prefix := "regtest" + strconv.FormatInt(time.Now().UnixNano(), 36)
	email := func(local string) string { return prefix + "+" + local + "@example.com" }
	var createdTenants []string
	t.Cleanup(func() {
		for _, id := range createdTenants {
			_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email LIKE $1`, prefix+"+%")
	})

	register := func(body map[string]any) (*http.Response, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+"/auth/register", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		if tid, ok := out["tenant_id"].(string); ok && tid != "" {
			createdTenants = append(createdTenants, tid)
		}
		return resp, out
	}

	// 1) 个人成功：默认名 "{账号@前} 的空间"，type=personal，落库一致。
	resp, out := register(map[string]any{
		"email": email("p1"), "password": "Str0ng-Passw0rd-9", "account_type": "personal",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("personal register status = %d body=%v", resp.StatusCode, out)
	}
	if out["tenant_type"] != "personal" || out["tenant_name"] != defaultPersonalTenantName(email("p1")) {
		t.Fatalf("personal register = %v", out)
	}
	var typ, name string
	if err := pool.QueryRow(ctx, `SELECT type, name FROM tenants WHERE id=$1`, out["tenant_id"]).Scan(&typ, &name); err != nil {
		t.Fatalf("load tenant: %v", err)
	}
	if typ != "personal" || name != defaultPersonalTenantName(email("p1")) {
		t.Fatalf("persisted tenant = type:%q name:%q", typ, name)
	}

	// 2) 个人携带 org_name 被忽略（不报错，仍用默认名）。
	resp, out = register(map[string]any{
		"email": email("p2"), "password": "Str0ng-Passw0rd-9",
		"account_type": "personal", "org_name": "Should Be Ignored",
	})
	if resp.StatusCode != http.StatusCreated || out["tenant_name"] != defaultPersonalTenantName(email("p2")) {
		t.Fatalf("personal with org_name = %d %v", resp.StatusCode, out)
	}

	// 3) 组织成功：采用传入的组织名。
	resp, out = register(map[string]any{
		"email": email("o1"), "password": "Str0ng-Passw0rd-9",
		"account_type": "organization", "org_name": "Acme 北京",
	})
	if resp.StatusCode != http.StatusCreated || out["tenant_type"] != "organization" || out["tenant_name"] != "Acme 北京" {
		t.Fatalf("organization register = %d %v", resp.StatusCode, out)
	}

	// 4) 组织缺名 / 非法字符 / 超长 → 400。
	for _, bad := range []string{"", "bad/name", strings.Repeat("x", maxOrgName+1)} {
		resp, _ = register(map[string]any{
			"email": email("o2"), "password": "Str0ng-Passw0rd-9",
			"account_type": "organization", "org_name": bad,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("org_name %q status = %d want 400", bad, resp.StatusCode)
		}
	}

	// 5) 缺失/非法 account_type → 400（不做猜测式兜底）。
	for _, bad := range []string{"", "team"} {
		resp, _ = register(map[string]any{
			"email": email("x1"), "password": "Str0ng-Passw0rd-9", "account_type": bad,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("account_type %q status = %d want 400", bad, resp.StatusCode)
		}
	}
}
