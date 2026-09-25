package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

const auditOtherTenant = "tenant-foreign"

// newSQLiteAuditStore 建立迁移后的 SQLite 审计库。返回底层 *sql.DB 是为了布置
// 第二个租户的行（audit_events.tenant_id 外键指向 tenants）。
func newSQLiteAuditStore(t *testing.T) (*SQLiteStore, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb), sqldb
}

// TestSQLiteAuditRoundTrip 守护 sqScanEvent 的 8 个 Scan 目标与列清单顺序一致。
//
// 错位是无法被编译器发现的：actor_user 读进 action、resource_id 读进 metadata 都不会报错，
// 只会让审计日志安静地说慌。所以这里每个字段都用互不相同的取值，错位必然导致断言失配。
func TestSQLiteAuditRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, _ := newSQLiteAuditStore(t)
	const tenant = db.LocalTenantID

	if err := store.Record(ctx, Event{
		TenantID:     tenant,
		ActorUser:    "u-actor",
		Action:       "project.create",
		ResourceType: "project",
		ResourceID:   "p-1",
		Metadata:     map[string]any{"title": "年度汇报", "pages": 12},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	list, err := store.List(ctx, tenant, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d want 1", len(list))
	}
	e := list[0]
	if e.TenantID != tenant {
		t.Fatalf("TenantID = %q", e.TenantID)
	}
	if e.ActorUser != "u-actor" {
		t.Fatalf("ActorUser = %q want u-actor", e.ActorUser)
	}
	if e.Action != "project.create" {
		t.Fatalf("Action = %q want project.create", e.Action)
	}
	if e.ResourceType != "project" {
		t.Fatalf("ResourceType = %q want project", e.ResourceType)
	}
	if e.ResourceID != "p-1" {
		t.Fatalf("ResourceID = %q want p-1", e.ResourceID)
	}
	if e.Metadata["title"] != "年度汇报" || e.Metadata["pages"] != float64(12) {
		t.Fatalf("Metadata 往返失真（JSON 未解析或列错位）：%#v", e.Metadata)
	}
	if e.ID == "" {
		t.Fatal("ID 未回填")
	}
	if e.CreatedAt.IsZero() {
		t.Fatal("CreatedAt 未解析")
	}

	// 未传 Action/Action 为空都是调用方 bug，必须显式报错而不是记一条无意义事件。
	if err := store.Record(ctx, Event{Action: "x"}); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("空 tenant err = %v want ErrTenantRequired", err)
	}
	if err := store.Record(ctx, Event{TenantID: tenant}); !errors.Is(err, ErrActionRequired) {
		t.Fatalf("空 action err = %v want ErrActionRequired", err)
	}
	if _, err := store.List(ctx, "", Filter{}); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("List 空 tenant err = %v want ErrTenantRequired", err)
	}
	if _, err := store.DeleteBefore(ctx, "", time.Now()); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("DeleteBefore 空 tenant err = %v want ErrTenantRequired", err)
	}
}

// TestSQLiteAuditFilterAndTenantScope 覆盖 List 的过滤与租户隔离：
// 过滤是"看到 subset"，租户隔离是"看不到别人"，两者失效都会让审计页给出错误结论。
func TestSQLiteAuditFilterAndTenantScope(t *testing.T) {
	ctx := context.Background()
	store, sqldb := newSQLiteAuditStore(t)
	const tenant = db.LocalTenantID

	if _, err := sqldb.ExecContext(ctx, `INSERT INTO tenants (id, name) VALUES (?, ?)`, auditOtherTenant, "另一租户"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	seed := []Event{
		{TenantID: tenant, Action: "script.generate", ResourceType: "script", ResourceID: "s-1"},
		{TenantID: tenant, Action: "script.generate", ResourceType: "project", ResourceID: "p-9"},
		{TenantID: tenant, Action: "job.cancel", ResourceType: "job", ResourceID: "j-1"},
		{TenantID: auditOtherTenant, Action: "secret.read", ResourceType: "credential", ResourceID: "c-1"},
	}
	for _, e := range seed {
		if err := store.Record(ctx, e); err != nil {
			t.Fatalf("record %s: %v", e.Action, err)
		}
	}

	all, err := store.List(ctx, tenant, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("本租户事件数 = %d want 3（若出现 4 说明跨租户泄露）", len(all))
	}
	for _, e := range all {
		if e.TenantID != tenant {
			t.Fatalf("List 返回了别的租户事件：%s", e.TenantID)
		}
	}

	byAction, err := store.List(ctx, tenant, Filter{Action: "script.generate"})
	if err != nil {
		t.Fatalf("list by action: %v", err)
	}
	if len(byAction) != 2 {
		t.Fatalf("action 过滤后 = %d want 2", len(byAction))
	}
	for _, e := range byAction {
		if e.Action != "script.generate" {
			t.Fatalf("action 过滤失效：%q", e.Action)
		}
	}

	byType, err := store.List(ctx, tenant, Filter{Action: "script.generate", ResourceType: "project"})
	if err != nil {
		t.Fatalf("list by type: %v", err)
	}
	if len(byType) != 1 || byType[0].ResourceID != "p-9" {
		t.Fatalf("组合过滤结果异常：%+v", byType)
	}
}

// TestSQLiteAuditDeleteBefore 覆盖归档清理路径：按时间裁剪，且只影响本租户。
func TestSQLiteAuditDeleteBefore(t *testing.T) {
	ctx := context.Background()
	store, _ := newSQLiteAuditStore(t)
	const tenant = db.LocalTenantID

	for _, a := range []string{"old.a", "old.b", "new.a"} {
		if err := store.Record(ctx, Event{TenantID: tenant, Action: a}); err != nil {
			t.Fatalf("record %s: %v", a, err)
		}
	}
	// created_at 精度为毫秒，睡一档确保能切开时间边界。
	time.Sleep(2 * time.Millisecond)
	fence := time.Now()
	if err := store.Record(ctx, Event{TenantID: tenant, Action: "new.b"}); err != nil {
		t.Fatalf("record new.b: %v", err)
	}

	n, err := store.DeleteBefore(ctx, tenant, fence)
	if err != nil {
		t.Fatalf("delete before: %v", err)
	}
	if n != 3 {
		t.Fatalf("删除行数 = %d want 3", n)
	}
	left, err := store.List(ctx, tenant, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(left) != 1 || left[0].Action != "new.b" {
		t.Fatalf("删除后残留异常：%+v", left)
	}
}
