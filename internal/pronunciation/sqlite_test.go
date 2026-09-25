package pronunciation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newSQLitePronunciationStore(t *testing.T) *SQLiteStore {
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
	return NewSQLiteStore(sqldb)
}

// TestSQLitePronunciationRoundTrip 守护 sqScanDict 的 6 个 Scan 目标与 sqDictColumns 顺序一致。
//
// 值得警惕的是 rules 列：它夹在两个同样表示时间戳的文本列之间（…rules, created_at, updated_at），
// 一旦顺序漂移，很可能表现为"规则读不出来"（降级为空集，见 ParseRules 的静默 nil）而不是报错——
// 词典看起来存在却没有效果，属于典型的假成功。因此本用例断言规则内容**逐条保真**。
func TestSQLitePronunciationRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newSQLitePronunciationStore(t)
	const tenant = db.LocalTenantID

	in := &Dictionary{
		ID:       "d-1",
		TenantID: tenant,
		Name:     "技术词典",
		Rules: Rules{
			{Pattern: "PPT", Replacement: "P P T", Enabled: true},
			{Pattern: "Go", Replacement: "Golang", Enabled: false},
		},
	}
	if err := store.Create(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetByID(ctx, tenant, "d-1")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	assertDict(t, "GetByID", got, in)

	list, err := store.ListByTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d want 1", len(list))
	}
	assertDict(t, "ListByTenant", list[0], in)
}

func assertDict(t *testing.T, path string, got, want *Dictionary) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("%s: ID = %q want %q", path, got.ID, want.ID)
	}
	if got.TenantID != want.TenantID {
		t.Fatalf("%s: TenantID = %q want %q", path, got.TenantID, want.TenantID)
	}
	if got.Name != want.Name {
		t.Fatalf("%s: Name = %q want %q", path, got.Name, want.Name)
	}
	if len(got.Rules) != len(want.Rules) {
		t.Fatalf("%s: rules len = %d want %d（规则丢失或错位）", path, len(got.Rules), len(want.Rules))
	}
	for i := range want.Rules {
		if got.Rules[i] != want.Rules[i] {
			t.Fatalf("%s: rules[%d] = %+v want %+v", path, i, got.Rules[i], want.Rules[i])
		}
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("%s: 时间戳未解析 CreatedAt=%d UpdatedAt=%d", path, got.CreatedAt, got.UpdatedAt)
	}
}

// TestSQLitePronunciationLifecycleAndScope 覆盖 Update/Delete 与跨租户边界。
func TestSQLitePronunciationLifecycleAndScope(t *testing.T) {
	ctx := context.Background()
	store := newSQLitePronunciationStore(t)
	const tenant = db.LocalTenantID
	const foreign = "tenant-foreign"

	if err := store.Create(ctx, &Dictionary{
		ID: "d-1", TenantID: tenant, Name: "原名",
		Rules: Rules{{Pattern: "a", Replacement: "b", Enabled: true}},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Create(ctx, &Dictionary{
		ID: "d-2", TenantID: foreign, Name: "别人的词典",
		Rules: Rules{{Pattern: "x", Replacement: "y", Enabled: true}},
	}); err != nil {
		t.Fatalf("create foreign: %v", err)
	}

	// Update 只应作用于 (tenant, id) 双重限定下的那一行。
	if err := store.Update(ctx, &Dictionary{
		ID: "d-1", TenantID: tenant, Name: "改名",
		Rules: Rules{{Pattern: "PPT", Replacement: "P P T", Enabled: true}},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetByID(ctx, tenant, "d-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "改名" {
		t.Fatalf("Update 未生效：name=%q", got.Name)
	}
	if len(got.Rules) != 1 || got.Rules[0].Pattern != "PPT" {
		t.Fatalf("Update 后规则不符：%+v", got.Rules)
	}

	// 跨租户：看不到、改不到、删不掉。
	if _, err := store.GetByID(ctx, foreign, "d-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨租户读取 err = %v want ErrNotFound", err)
	}
	if err := store.Update(ctx, &Dictionary{ID: "d-2", TenantID: tenant, Name: "改名别人的"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨租户更新 err = %v want ErrNotFound", err)
	}
	if err := store.Delete(ctx, tenant, "d-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨租户删除 err = %v want ErrNotFound", err)
	}
	list, err := store.ListByTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("本租户词典数 = %d want 1（跨租户泄露）", len(list))
	}

	if err := store.Delete(ctx, tenant, "d-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetByID(ctx, tenant, "d-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后 err = %v want ErrNotFound", err)
	}
	if err := store.Delete(ctx, tenant, "d-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除 err = %v want ErrNotFound", err)
	}
}

// TestSQLitePronunciationLoadTenantDefault 覆盖「无词典 → 空集」与「取最新一条」两条分支。
// 空集必须返回 nil 而不是报错——它是正常业务状态，误报会让 TTS 前置流程整体失败。
func TestSQLitePronunciationLoadTenantDefault(t *testing.T) {
	ctx := context.Background()
	store := newSQLitePronunciationStore(t)
	const tenant = db.LocalTenantID

	rules, err := store.LoadTenantDefault(ctx, tenant)
	if err != nil {
		t.Fatalf("空库 load default: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("无词典时应返回空集，实际 %+v", rules)
	}

	if err := store.Create(ctx, &Dictionary{
		ID: "d-old", TenantID: tenant, Name: "旧",
		Rules: Rules{{Pattern: "old", Replacement: "OLD", Enabled: true}},
	}); err != nil {
		t.Fatalf("create old: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // created_at 为毫秒精度，错开排序依据
	if err := store.Create(ctx, &Dictionary{
		ID: "d-new", TenantID: tenant, Name: "新",
		Rules: Rules{{Pattern: "new", Replacement: "NEW", Enabled: true}},
	}); err != nil {
		t.Fatalf("create new: %v", err)
	}

	rules, err = store.LoadTenantDefault(ctx, tenant)
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if len(rules) != 1 || rules[0].Pattern != "new" {
		t.Fatalf("应取最新一条规则，实际 %+v", rules)
	}
}
