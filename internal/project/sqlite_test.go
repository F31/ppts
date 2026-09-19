package project

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

const (
	testTenant = db.LocalTenantID
	testOwner  = db.LocalUserID
	testOther  = "other-user"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "ppts.db")
	sqldb, err := db.OpenSQLite(ctx, dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("sqlite fs: %v", err)
	}
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("local identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

func TestSQLiteProjectCRUDAndACL(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	p, err := s.CreateProject(ctx, testTenant, testOwner, "我的项目")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" || p.TenantID != testTenant || p.OwnerUser != testOwner {
		t.Fatalf("unexpected project: %+v", p)
	}

	// owner 可见
	got, err := s.GetProject(ctx, testTenant, testOwner, p.ID)
	if err != nil || got.ID != p.ID {
		t.Fatalf("owner get: %v %+v", err, got)
	}
	// 非协作者不可见
	if _, err := s.GetProject(ctx, testTenant, testOther, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("non-collaborator should be denied, got %v", err)
	}
	// 管理旁路（userID=""）可见
	if _, err := s.GetProject(ctx, testTenant, "", p.ID); err != nil {
		t.Fatalf("admin bypass should see project: %v", err)
	}

	// 列表：owner 1 条，非协作者 0 条，旁路 1 条
	list, _, err := s.ListProjects(ctx, testTenant, testOwner, "", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("owner list: %v n=%d", err, len(list))
	}
	list, _, err = s.ListProjects(ctx, testTenant, testOther, "", 10)
	if err != nil || len(list) != 0 {
		t.Fatalf("non-member list should be empty: %v n=%d", err, len(list))
	}
	list, _, err = s.ListProjects(ctx, testTenant, "", "", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("bypass list: %v n=%d", err, len(list))
	}

	// 归档
	archived, err := s.ArchiveProject(ctx, testTenant, testOwner, p.ID)
	if err != nil || !archived.Archived {
		t.Fatalf("archive: %v %+v", err, archived)
	}
}

func TestSQLiteSourceRevisions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, testTenant, testOwner, "P")

	in := NewSourceRevision{ProjectID: p.ID, SourceHash: "h1", ObjectKey: "o1", ParserVersion: "v1", UploadID: "up-1"}
	rev, err := s.CreateSourceRevision(ctx, testTenant, in)
	if err != nil {
		t.Fatalf("create rev: %v", err)
	}
	if rev.RevisionNo != 1 {
		t.Fatalf("want revision 1, got %d", rev.RevisionNo)
	}
	// 幂等：同 upload_id 返回既有
	again, err := s.CreateSourceRevision(ctx, testTenant, in)
	if err != nil || again.ID != rev.ID {
		t.Fatalf("idempotent revision: err=%v id=%s want=%s", err, again.ID, rev.ID)
	}
	// current_revision 应更新为 1
	got, _ := s.GetProject(ctx, testTenant, testOwner, p.ID)
	if got.CurrentRevision != 1 {
		t.Fatalf("current_revision want 1 got %d", got.CurrentRevision)
	}
	// 第二次真正的新版本
	rev2, err := s.CreateSourceRevision(ctx, testTenant,
		NewSourceRevision{ProjectID: p.ID, SourceHash: "h2", ObjectKey: "o2", ParserVersion: "v1"})
	if err != nil || rev2.RevisionNo != 2 {
		t.Fatalf("second revision: err=%v no=%d", err, rev2.RevisionNo)
	}
	revs, err := s.ListSourceRevisions(ctx, testTenant, p.ID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("list revisions: err=%v n=%d", err, len(revs))
	}
	if _, err := s.GetSourceRevision(ctx, testTenant, p.ID, 1); err != nil {
		t.Fatalf("get rev 1: %v", err)
	}
}

func TestSQLiteTagsFolders(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, testTenant, testOwner, "P")

	tag, err := s.CreateTag(ctx, testTenant, "紧急", "#f00")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if _, err := s.CreateTag(ctx, testTenant, "紧急", ""); !errors.Is(err, ErrTagNameExists) {
		t.Fatalf("duplicate tag should conflict, got %v", err)
	}
	if err := s.AttachTag(ctx, testTenant, p.ID, tag.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}
	orgs, err := s.ListProjectOrganization(ctx, testTenant, testOwner)
	if err != nil || len(orgs) != 1 || len(orgs[0].TagIDs) != 1 {
		t.Fatalf("org after attach: err=%v orgs=%+v", err, orgs)
	}
	if err := s.DetachTag(ctx, testTenant, p.ID, tag.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}

	folder, err := s.CreateFolder(ctx, testTenant, "客户A", testOwner)
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := s.MoveProject(ctx, testTenant, p.ID, folder.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	orgs, _ = s.ListProjectOrganization(ctx, testTenant, testOwner)
	if len(orgs) != 1 || orgs[0].FolderID != folder.ID {
		t.Fatalf("folder not set: %+v", orgs)
	}
	// 删分组：有项目时禁止删除。
	if err := s.DeleteFolder(ctx, testTenant, folder.ID); !errors.Is(err, ErrFolderNotEmpty) {
		t.Fatalf("delete folder with projects: got %v want ErrFolderNotEmpty", err)
	}
	// 移走项目后再删 → 成功。
	if err := s.MoveProject(ctx, testTenant, p.ID, ""); err != nil {
		t.Fatalf("move to uncat: %v", err)
	}
	if err := s.DeleteFolder(ctx, testTenant, folder.ID); err != nil {
		t.Fatalf("delete empty folder: %v", err)
	}
	orgs, _ = s.ListProjectOrganization(ctx, testTenant, testOwner)
	if len(orgs) != 1 || orgs[0].FolderID != "" {
		t.Fatalf("folder should reset to empty: %+v", orgs)
	}
}

func TestSQLiteCollaboratorsAndShareLinks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p, _ := s.CreateProject(ctx, testTenant, testOwner, "P")

	// 邀请协作者
	if _, err := s.InviteCollaborator(ctx, testTenant, p.ID, testOther, CollabRoleEditor, testOwner); err != nil {
		t.Fatalf("invite: %v", err)
	}
	// 协作者即可见
	if _, err := s.GetProject(ctx, testTenant, testOther, p.ID); err != nil {
		t.Fatalf("collaborator should see project: %v", err)
	}
	// 更新角色
	if _, err := s.UpdateCollaboratorRole(ctx, testTenant, p.ID, testOther, CollabRoleViewer); err != nil {
		t.Fatalf("update role: %v", err)
	}
	// 非法角色
	if _, err := s.UpdateCollaboratorRole(ctx, testTenant, p.ID, testOther, "owner"); !errors.Is(err, ErrInvalidCollaboratorRole) {
		t.Fatalf("owner role should be invalid at project level, got %v", err)
	}
	// 移除 → 不可见
	if err := s.RemoveCollaborator(ctx, testTenant, p.ID, testOther); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := s.GetProject(ctx, testTenant, testOther, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("removed collaborator should be denied, got %v", err)
	}

	// 分享链接（无口令）
	link, err := s.CreateShareLink(ctx, testTenant, p.ID, ShareAccessViewOnly, "", testOwner, nil)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	if link.Token == "" || link.PasswordProtected {
		t.Fatalf("unexpected link: %+v", link)
	}
	if _, err := s.GetShareLinkByToken(ctx, link.Token); err != nil {
		t.Fatalf("get by token: %v", err)
	}
	// 撤回后不可见
	if err := s.RevokeShareLink(ctx, testTenant, link.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.GetShareLinkByToken(ctx, link.Token); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("revoked link should be not found, got %v", err)
	}

	// 带口令 + 过期
	past := time.Now().Add(-time.Hour)
	expired, err := s.CreateShareLink(ctx, testTenant, p.ID, ShareAccessViewAndComment, "bcrypt-hash", testOwner, &past)
	if err != nil {
		t.Fatalf("create expired link: %v", err)
	}
	if !expired.PasswordProtected {
		t.Fatal("link should be password protected")
	}
	if _, err := s.GetShareLinkByToken(ctx, expired.Token); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("expired link should be not found, got %v", err)
	}
	hash, err := s.ShareLinkPasswordHash(ctx, testTenant, expired.ID)
	if err != nil || hash != "bcrypt-hash" {
		t.Fatalf("password hash: err=%v hash=%q", err, hash)
	}
}

// 保证 SQLite 实现满足 ProjectStore 端口契约。
var _ ProjectStore = (*SQLiteStore)(nil)

// 显式使用 sql 包避免未使用告警（测试辅助中可能未直接引用）。
var _ = sql.ErrNoRows
