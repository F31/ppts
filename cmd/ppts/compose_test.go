package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
)

// TestOpenStoresSQLiteSmoke 验证 SQLite 单租户组合根：默认驱动、自动迁移/播种、
// 本地身份、以及通过 store 集建项目/列项目的端到端可用性。
func TestOpenStoresSQLiteSmoke(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PPTS_DB_DRIVER", "") // 留空 → 默认 sqlite
	t.Setenv("PPTS_DATABASE_URL", "")
	t.Setenv("HOME", dir)
	t.Setenv("PPTS_OBJECT_BACKEND", "local")
	t.Setenv("PPTS_OBJECT_ROOT", filepath.Join(dir, "objects"))
	t.Setenv("PPTS_OBJECT_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 32)))

	ctx := context.Background()
	stores, err := openStores(ctx)
	if err != nil {
		t.Fatalf("openStores: %v", err)
	}
	defer stores.close()

	if !stores.isSQLite() {
		t.Fatalf("expected sqlite driver, got %s", stores.cfg.Driver)
	}
	if stores.localPrincipalFor() == nil {
		t.Fatal("sqlite mode should expose a local principal")
	}
	if got := stores.defaultWorkerTenant(""); got != db.LocalTenantID {
		t.Fatalf("worker tenant = %q want %q", got, db.LocalTenantID)
	}

	// 建项目（组合根 + SQLite 端到端）
	p, err := stores.projects.CreateProject(ctx, db.LocalTenantID, db.LocalUserID, "冒烟项目")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected project id")
	}
	list, _, err := stores.projects.ListProjects(ctx, db.LocalTenantID, db.LocalUserID, "", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list projects: err=%v n=%d", err, len(list))
	}

	// 本地用户角色为 owner
	role, err := stores.members.GetRole(ctx, db.LocalTenantID, db.LocalUserID)
	if err != nil || string(role) != "owner" {
		t.Fatalf("local user role: err=%v role=%s", err, role)
	}

	// 租户状态可服务
	active, err := stores.tenant.TenantActive(ctx, db.LocalTenantID)
	if err != nil || !active {
		t.Fatalf("tenant should be active: err=%v active=%v", err, active)
	}
}

// TestOpenStoresPostgresRequiresDSN 验证 postgres 驱动缺少 DSN 时明确报错。
func TestOpenStoresPostgresRequiresDSN(t *testing.T) {
	t.Setenv("PPTS_DB_DRIVER", "postgres")
	t.Setenv("PPTS_DATABASE_URL", "")
	if _, err := openStores(context.Background()); err == nil {
		t.Fatal("postgres without DSN should error")
	}
}
