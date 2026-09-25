package membership

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

// newSQLiteMembershipStore 建立一个跑完迁移并播种本地身份的 SQLite 成员库。
// 返回 *sql.DB 是为了让用例直接布置联表所需的 users / tenants 行——List 的正确性
// 恰恰取决于 LEFT JOIN 的三张表都真的有数据。
func newSQLiteMembershipStore(t *testing.T) (*SQLiteStore, *sql.DB) {
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

func mustMemberExec(t *testing.T, sqldb *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := sqldb.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestSQLiteMembershipRoundTrip 守护"列清单顺序 == Scan 目标顺序"这一不可见约束。
//
// SQLiteStore.List 一次选出 9 列：tenant_members 的 user_id/role/created_at，
// 再 LEFT JOIN users.email 与 user_profiles 的 5 个档案列。真一个 Scan 目标错位，
// 编译期毫无察觉，运行时会把 email 读进 Username、把 phone 读进 Gender 之类——
// 界面照常显示，只是每个人的信息都是别人的。
//
// 因此这里给每个字段都用**互不相同且类型可辨**的值：一旦顺序漂移，等值断言必然失配。
func TestSQLiteMembershipRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, sqldb := newSQLiteMembershipStore(t)
	const tenant = db.LocalTenantID

	mustMemberExec(t, sqldb, `INSERT INTO users (id, email) VALUES (?, ?)`, "u-1", "member1@example.com")
	mustMemberExec(t, sqldb, `INSERT INTO tenant_members (tenant_id, user_id, role) VALUES (?, ?, ?)`,
		tenant, "u-1", string(RoleAdmin))

	if err := store.SaveProfile(ctx, tenant, "u-1", Profile{
		Username:  "zhangsan",
		FullName:  "张三",
		Gender:    "male",
		BirthDate: "1990-01-02",
		Phone:     "13800000000",
	}); err != nil {
		t.Fatalf("save profile: %v", err)
	}

	role, err := store.GetRole(ctx, tenant, "u-1")
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	if role != RoleAdmin {
		t.Fatalf("role = %q want %q", role, RoleAdmin)
	}

	list, err := store.List(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 本地身份还播种了一位 owner，见 internal/db.EnsureLocalIdentity。
	if len(list) != 2 {
		t.Fatalf("list len = %d want 2", len(list))
	}
	var got Member
	for _, m := range list {
		if m.UserID == "u-1" {
			got = m
		}
	}
	if got.UserID == "" {
		t.Fatal("list 未包含 u-1")
	}

	if got.Role != RoleAdmin {
		t.Fatalf("UserID/Role 错位：role=%q", got.Role)
	}
	if got.Email != "member1@example.com" {
		t.Fatalf("email = %q（若为空说明 email 未回填；若读到档案值说明列错位）", got.Email)
	}
	if got.Username != "zhangsan" {
		t.Fatalf("username = %q want zhangsan", got.Username)
	}
	if got.FullName != "张三" {
		t.Fatalf("full_name = %q want 张三", got.FullName)
	}
	if got.Gender != "male" {
		t.Fatalf("gender = %q want male", got.Gender)
	}
	if got.BirthDate != "1990-01-02" {
		t.Fatalf("birth_date = %q want 1990-01-02", got.BirthDate)
	}
	if got.Phone != "13800000000" {
		t.Fatalf("phone = %q want 13800000000", got.Phone)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at 未解析（Scan 目标错位或时间格式不符）")
	}

	if err := store.Remove(ctx, tenant, "u-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := store.GetRole(ctx, tenant, "u-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove 后 GetRole err = %v want ErrNotFound", err)
	}
	if err := store.Remove(ctx, tenant, "u-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复 Remove err = %v want ErrNotFound", err)
	}
}

// TestSQLiteSaveProfileEnforcesTenantScope 守护跨租户写入防线（Phase 0.4 / R4）。
//
// user_profiles 没有租户列，单看表结构无法判断某一行属于谁；因此**写入前**必须先确认
// 目标 userID 是本租户成员，否则任一租户的 Admin 都能改写已知 userID 的档案。
// 缺失这层校验时，本用例的第一步断言就会失败。
func TestSQLiteSaveProfileEnforcesTenantScope(t *testing.T) {
	ctx := context.Background()
	store, sqldb := newSQLiteMembershipStore(t)
	const tenant = db.LocalTenantID

	// 一个属于别的租户的用户（ tenant_members 有外键，先落 tenants 行）。
	mustMemberExec(t, sqldb, `INSERT INTO tenants (id, name) VALUES (?, ?)`, "tenant-2", "另一租户")
	mustMemberExec(t, sqldb, `INSERT INTO users (id, email) VALUES (?, ?)`, "u-foreign", "foreign@example.com")
	mustMemberExec(t, sqldb, `INSERT INTO tenant_members (tenant_id, user_id, role) VALUES (?, ?, ?)`,
		"tenant-2", "u-foreign", string(RoleViewer))

	err := store.SaveProfile(ctx, tenant, "u-foreign", Profile{Username: "intruder", FullName: "越权者"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("写入外租户成员档案应被拒绝，实际 err=%v", err)
	}
	var n int
	if err := sqldb.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_profiles WHERE user_id = ?`, "u-foreign").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("跨租户档案竟然落库了（%d 行）", n)
	}

	// 对照组：本租户成员照常写入——只写"拒绝"的用例无法发现把功能一起封死的回归。
	mustMemberExec(t, sqldb, `INSERT INTO users (id, email) VALUES (?, ?)`, "u-own", "own@example.com")
	mustMemberExec(t, sqldb, `INSERT INTO tenant_members (tenant_id, user_id, role) VALUES (?, ?, ?)`,
		tenant, "u-own", string(RoleEditor))
	if err := store.SaveProfile(ctx, tenant, "u-own", Profile{Username: "lisi", Phone: "13900000000"}); err != nil {
		t.Fatalf("本租户成员写入应成功，实际 err=%v", err)
	}
	list, err := store.List(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, m := range list {
		if m.UserID == "u-own" && m.Username == "lisi" && m.Phone == "13900000000" {
			found = true
		}
	}
	if !found {
		t.Fatalf("本租户档案未通过 List 读回：%+v", list)
	}

	// 空租户 / 空用户是调用方 bug，必须显式报错而不是"悄悄写成孤儿行"。
	if err := store.SaveProfile(ctx, "", "u-own", Profile{}); err == nil {
		t.Fatal("空 tenantID 应报错")
	}
	if err := store.SaveProfile(ctx, tenant, "   ", Profile{}); err == nil {
		t.Fatal("空 userID 应报错")
	}
}
