package upload

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

func newSQLiteUploadStore(t *testing.T) *SQLiteStore {
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

func newTestUpload(tenant, id string) NewUpload {
	return NewUpload{
		ID:                id,
		TenantID:          tenant,
		ProjectID:         "p-1",
		Filename:          "年度汇报.pptx",
		ContentType:       "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		ObjectKey:         tenant + "/p-1/source/" + id + ".pptx",
		SizeBytes:         2_048_576,
		DeleteSourceAfter: true,
	}
}

// TestSQLiteUploadRoundTrip 守护 sqScanUpload 的 12 个 Scan 目标与 sqUploadColumns 顺序一致。
//
// 最易错位的一段是 `… size_bytes, delete_source_after, state, object_key, …`：
// delete_source_after 是 INTEGER→bool 的手工转换，state/object_key 都是字符串。
// 顺序一旦漂移，bool 与字符串会互相串味，而 Go 的 SQLite 驱动在多数类型间转换是宽容的，
// 所以错位很可能表现为"状态是对的、对象键是布尔想出来的样子"，而不是报错。
// 故每个字段都取值各异，并额外断言 bool 转换方向。
func TestSQLiteUploadRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteUploadStore(t)
	const tenant = db.LocalTenantID
	in := newTestUpload(tenant, "up-1")

	created, err := store.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.State != StatePending {
		t.Fatalf("新建会话 state = %q want pending", created.State)
	}
	assertUploadSession(t, "Create", created, in, false)

	got, err := store.Get(ctx, tenant, "up-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertUploadSession(t, "Get", got, in, false)

	if _, err := store.Get(ctx, tenant, "up-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的会话 err = %v want ErrNotFound", err)
	}
	// 跨租户：另一个租户 ID 查同一条必须视为不存在，而不是返回别人的会话。
	if _, err := store.Get(ctx, "tenant-foreign", "up-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("跨租户读取 err = %v want ErrNotFound", err)
	}
}

func assertUploadSession(t *testing.T, path string, got *UploadSession, want NewUpload, terminal bool) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("%s: ID = %q want %q", path, got.ID, want.ID)
	}
	if got.TenantID != want.TenantID {
		t.Fatalf("%s: TenantID = %q want %q", path, got.TenantID, want.TenantID)
	}
	if got.ProjectID != want.ProjectID {
		t.Fatalf("%s: ProjectID = %q want %q", path, got.ProjectID, want.ProjectID)
	}
	if got.Filename != want.Filename {
		t.Fatalf("%s: Filename = %q want %q", path, got.Filename, want.Filename)
	}
	if got.ContentType != want.ContentType {
		t.Fatalf("%s: ContentType = %q want %q", path, got.ContentType, want.ContentType)
	}
	if got.ObjectKey != want.ObjectKey {
		t.Fatalf("%s: ObjectKey = %q want %q（状态/对象键列错位时会串）", path, got.ObjectKey, want.ObjectKey)
	}
	if got.SizeBytes != want.SizeBytes {
		t.Fatalf("%s: SizeBytes = %d want %d", path, got.SizeBytes, want.SizeBytes)
	}
	if got.DeleteSourceAfter != want.DeleteSourceAfter {
		t.Fatalf("%s: DeleteSourceAfter = %v want %v（整/布 转换方向反了或列错位）", path, got.DeleteSourceAfter, want.DeleteSourceAfter)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("%s: 时间戳未解析 created=%v updated=%v", path, got.CreatedAt, got.UpdatedAt)
	}
	if terminal {
		if got.UpdatedAt.Before(got.CreatedAt) {
			t.Fatalf("%s: UpdatedAt 早于 CreatedAt", path)
		}
	}
}

// TestSQLiteUploadStateMachine 覆盖 Complete/Abort 的幂等与互斥语义。
// 这两条不容易被"列错序"牵连，但一旦退化，用户会看到"完成/中止"被静默覆盖——
// 也是 A26 关注的那一类：把状态冲突当成成功。
func TestSQLiteUploadStateMachine(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteUploadStore(t)
	const tenant = db.LocalTenantID

	if _, err := store.Create(ctx, newTestUpload(tenant, "up-complete")); err != nil {
		t.Fatalf("create: %v", err)
	}
	done, err := store.Complete(ctx, tenant, "up-complete", "rev-7", "job-9")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.State != StateCompleted {
		t.Fatalf("state = %q want completed", done.State)
	}
	if done.SourceRevisionID != "rev-7" || done.JobID != "job-9" {
		t.Fatalf("终端字段未落库：rev=%q job=%q", done.SourceRevisionID, done.JobID)
	}
	// 幂等：重复 Complete 返回同一状态，而不是把已完成的会话判为失败。
	again, err := store.Complete(ctx, tenant, "up-complete", "rev-7", "job-9")
	if err != nil {
		t.Fatalf("重复 complete 应幂等，实际 err=%v", err)
	}
	if again.State != StateCompleted {
		t.Fatalf("重复 complete 后 state = %q", again.State)
	}
	// 但完成与中止互斥：已完成后再中止必须报错。
	if _, err := store.Abort(ctx, tenant, "up-complete"); !errors.Is(err, ErrAlreadyCompleted) {
		t.Fatalf("已完成会话 abort err = %v want ErrAlreadyCompleted", err)
	}

	if _, err := store.Create(ctx, newTestUpload(tenant, "up-abort")); err != nil {
		t.Fatalf("create abort: %v", err)
	}
	aborted, err := store.Abort(ctx, tenant, "up-abort")
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.State != StateAborted {
		t.Fatalf("state = %q want aborted", aborted.State)
	}
	if _, err := store.Complete(ctx, tenant, "up-abort", "rev-x", "job-x"); !errors.Is(err, ErrAlreadyAborted) {
		t.Fatalf("已中止会话 complete err = %v want ErrAlreadyAborted", err)
	}
}
