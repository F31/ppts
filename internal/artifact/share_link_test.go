package artifact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// newShareLinkTestStore 复用 sqlite_test.go 的建库方式，额外返回底层 *sql.DB
// 以便直接删行验证级联（模拟「成品被删除」这条真实路径）。
func newShareLinkTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	return newSQLiteArtifactStore(t)
}

func mustArtifact(t *testing.T, s *SQLiteStore, projectID, format string) *Artifact {
	t.Helper()
	a, err := s.Create(context.Background(), "local", NewArtifact{
		ProjectID: projectID, SnapshotHash: "snap-" + projectID, Format: Format(format),
		ObjectKey: "k/" + projectID + ".zip", ContentHash: "c-" + projectID, SizeBytes: 10,
	})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	return a
}

func TestShareLinkRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newShareLinkTestStore(t)
	a := mustArtifact(t, s, "p1", "html_bundle")

	expires := time.Now().Add(48 * time.Hour)
	l, err := s.CreateShareLink(ctx, "local", NewShareLink{
		ArtifactID: a.ID, ProjectID: "p1", AccessMode: "view_and_comment",
		PasswordHash: "bcrypt-hash", CreatedBy: "u1", ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatalf("CreateShareLink: %v", err)
	}

	// token 必须是 32 位十六进制：长度或字符集一改，匿名端点的路径解析与
	// 运维排查脚本都会跟着失效，所以把形状也钉住。
	if len(l.Token) != 32 || strings.Trim(l.Token, "0123456789abcdef") != "" {
		t.Fatalf("token = %q, want 32 hex chars", l.Token)
	}
	if !l.PasswordProtected {
		t.Fatal("passwordProtected should be true when a hash is supplied")
	}
	if l.AccessMode != "view_and_comment" {
		t.Fatalf("accessMode = %q", l.AccessMode)
	}

	got, err := s.GetShareLinkByToken(ctx, l.Token)
	if err != nil {
		t.Fatalf("GetShareLinkByToken: %v", err)
	}
	// 逐个断言易错列：这类错位在编译期不可见（本包已踩过一次）。
	if got.ID != l.ID || got.ArtifactID != a.ID || got.ProjectID != "p1" {
		t.Fatalf("identity drift: %+v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires.Truncate(time.Millisecond)) {
		t.Fatalf("expiresAt = %v, want %v", got.ExpiresAt, expires)
	}
	if got.LastAccessedAt != nil {
		t.Fatalf("lastAccessedAt should start nil, got %v", got.LastAccessedAt)
	}

	// 匿名读路径不能读到哈希。
	items, err := s.ListShareLinks(ctx, "local", a.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListShareLinks = %d, %v", len(items), err)
	}
	if items[0].CreatedBy != "u1" {
		t.Fatalf("createdBy = %q", items[0].CreatedBy)
	}
}

func TestShareLinkRevokeAndExpiryCloseTheDoor(t *testing.T) {
	ctx := context.Background()
	s := newShareLinkTestStore(t)
	a := mustArtifact(t, s, "p2", "html_bundle")

	live, err := s.CreateShareLink(ctx, "local", NewShareLink{ArtifactID: a.ID, ProjectID: "p2"})
	if err != nil {
		t.Fatalf("create live: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := s.CreateShareLink(ctx, "local", NewShareLink{
		ArtifactID: a.ID, ProjectID: "p2", ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	var expiredToken string
	items, _ := s.ListShareLinks(ctx, "local", a.ID)
	for _, item := range items {
		if item.ExpiresAt != nil {
			expiredToken = item.Token
		}
	}
	if expiredToken == "" {
		t.Fatal("expired link not returned by ListShareLinks")
	}

	if _, err := s.GetShareLinkByToken(ctx, expiredToken); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("expired token err = %v, want ErrShareLinkNotFound", err)
	}

	if err := s.RevokeShareLink(ctx, "local", live.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.GetShareLinkByToken(ctx, live.Token); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("revoked token err = %v, want ErrShareLinkNotFound", err)
	}
	// 撤销后行仍在，管理端要能看到它（否则「撤销过哪几条」无从审计）。
	if items, _ := s.ListShareLinks(ctx, "local", a.ID); len(items) != 2 {
		t.Fatalf("revoked link disappeared from list: %d", len(items))
	}
	// 撤销不存在的链接要报错，不能静默成功。
	if err := s.RevokeShareLink(ctx, "local", "no-such-id"); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("revoke missing err = %v", err)
	}
}

// TestShareLinkUsableAtMatchesSQL 是两套方言的语义对齐门禁。
//
// SQL 侧（PG 的 now() 与 SQLite 的参数）与 Go 侧 UsableAt 是两份实现，
// 这里用同一张矩阵断言它们对同一行的判断完全一致。漂移方向几乎总是
// 「库里的行已经撤了，代码还认为有效」，所以矩阵必须含 revoked/expired 的所有组合。
func TestShareLinkUsableAtMatchesSQL(t *testing.T) {
	ctx := context.Background()
	s := newShareLinkTestStore(t)
	a := mustArtifact(t, s, "p3", "html_bundle")

	future := time.Now().Add(time.Hour)
	cases := []struct {
		name    string
		revoke  bool
		expires *time.Time
		want    bool
	}{
		{"live-forever", false, nil, true},
		{"live-with-future-expiry", false, &future, true},
		{"revoked", true, nil, false},
		{"revoked-with-future-expiry", true, &future, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := s.CreateShareLink(ctx, "local", NewShareLink{
				ArtifactID: a.ID, ProjectID: "p3", ExpiresAt: tc.expires,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if tc.revoke {
				if err := s.RevokeShareLink(ctx, "local", l.ID); err != nil {
					t.Fatalf("revoke: %v", err)
				}
				l.Revoked = true
			}
			_, sqlErr := s.GetShareLinkByToken(ctx, l.Token)
			sqlUsable := sqlErr == nil
			if sqlUsable != tc.want {
				t.Fatalf("SQL usable = %v, want %v (err=%v)", sqlUsable, tc.want, sqlErr)
			}
			if goUsable := l.UsableAt(time.Now()); goUsable != tc.want {
				t.Fatalf("UsableAt = %v, want %v", goUsable, tc.want)
			}
		})
	}
}

// TestShareLinkCascadeWithArtifact 守的是「成品删除后链接自动消失」。
// 这条依赖外键级联，而不是应用层清理——没有它就得写一个删成品时顺手删链接的协程，
// 而那个协程一定会漏（比如成品由 retention 扫描批量删除的路径）。
func TestShareLinkCascadeWithArtifact(t *testing.T) {
	ctx := context.Background()
	s := newShareLinkTestStore(t)
	a := mustArtifact(t, s, "p4", "html_bundle")
	if _, err := s.CreateShareLink(ctx, "local", NewShareLink{ArtifactID: a.ID, ProjectID: "p4"}); err != nil {
		t.Fatalf("create link: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("delete artifact: %v", err)
	}
	items, err := s.ListShareLinks(ctx, "local", a.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("links survived artifact deletion: %d", len(items))
	}
}

// TestShareLinkColumnListsStayInSync 是 PG 与 SQLite 列清单的漂移门禁。
// 两边列清单必须逐字同序，否则「本地全绿、线上读错字段」这类故障会在发布后才出现。
func TestShareLinkColumnListsStayInSync(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if norm(shareLinkColumns) != norm(sqShareLinkColumns) {
		t.Fatalf("column lists drifted:\n pg: %s\n sq: %s",
			norm(shareLinkColumns), norm(sqShareLinkColumns))
	}
	// 匿名读路径不能带哈希——这条如果失守，是安全回归而不是功能回归。
	if strings.Contains(norm(shareLinkColumns), "password_hash") {
		t.Fatal("shareLinkColumns must not include password_hash")
	}
}
