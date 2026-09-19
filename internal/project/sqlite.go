package project

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 ProjectStore 的 SQLite 实现（单租户精简 profile）。
//
// 与 PGProjectStore 的差异：
//   - 不依赖 RLS：tenant_id 由调用方显式传入并落在 WHERE 子句；
//   - 无 gen_random_uuid()/now() 默认：ID 与时间戳由本层生成；
//   - 无 FOR UPDATE：SQLite 单写者，事务本身即串行化；
//   - userID 为空串仍表示"管理旁路"（不过滤 ACL），与 PG 语义保持一致，便于上层复用。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 项目存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

func sqNow() string { return db.Now() }

// ─── 扫描辅助 ────────────────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func sqScanProject(row rowScanner) (*Project, error) {
	var p Project
	var createdAt, updatedAt string
	var archived, deleteSource int
	if err := row.Scan(&p.ID, &p.TenantID, &p.OwnerUser, &p.Title, &p.CurrentRevision,
		&p.Policy, &archived, &deleteSource, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p.Archived = archived != 0
	p.DeleteSourceAfter = deleteSource != 0
	p.CreatedAt = db.ParseTime(createdAt)
	p.UpdatedAt = db.ParseTime(updatedAt)
	return &p, nil
}

const sqProjectColumns = `id, tenant_id, owner_user, title, current_revision, policy, archived,
	delete_source_after, created_at, updated_at`

func sqScanSourceRevision(row rowScanner) (*SourceRevision, error) {
	var r SourceRevision
	var createdAt string
	var sourceDeletedAt sql.NullString
	if err := row.Scan(&r.ID, &r.ProjectID, &r.TenantID, &r.RevisionNo, &r.SourceHash,
		&r.ObjectKey, &r.ParserVersion, &r.PageCount, &r.UploadID, &sourceDeletedAt, &createdAt); err != nil {
		return nil, err
	}
	r.CreatedAt = db.ParseTime(createdAt)
	return &r, nil
}

const sqSourceRevisionColumns = `id, project_id, tenant_id, revision_no, source_hash, object_key,
	parser_version, page_count, upload_id, source_deleted_at, created_at`

func sqNormalizeErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProjectNotFound
	}
	return err
}

// ─── 项目 CRUD ───────────────────────────────────────────────────────────────

func (s *SQLiteStore) CreateProject(ctx context.Context, tenantID, owner, title string) (*Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	id := uuid.New().String()
	now := sqNow()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO projects (id, tenant_id, owner_user, title, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, id, tenantID, owner, title, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_collaborators (tenant_id, project_id, user_id, role, invited_by)
		 VALUES (?, ?, ?, 'admin', ?) ON CONFLICT(project_id, user_id) DO NOTHING`,
		tenantID, id, owner, owner); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Project{ID: id, TenantID: tenantID, OwnerUser: owner, Title: title,
		Policy: []byte("{}"), CreatedAt: db.ParseTime(now), UpdatedAt: db.ParseTime(now)}, nil
}

func (s *SQLiteStore) GetProject(ctx context.Context, tenantID, userID, id string) (*Project, error) {
	p, err := sqScanProject(s.db.QueryRowContext(ctx,
		`SELECT `+sqProjectColumns+` FROM projects
		 WHERE id = ? AND tenant_id = ?
		   AND (? = '' OR owner_user = ? OR EXISTS (
		     SELECT 1 FROM project_collaborators pc
		      WHERE pc.project_id = projects.id AND pc.user_id = ?))`,
		id, tenantID, userID, userID, userID))
	return p, sqNormalizeErr(err)
}

func (s *SQLiteStore) ListProjects(ctx context.Context, tenantID, userID, cursor string, pageSize int) ([]*Project, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	acl := `(? = '' OR owner_user = ? OR EXISTS (
	          SELECT 1 FROM project_collaborators pc
	           WHERE pc.project_id = projects.id AND pc.user_id = ?))`
	var rows *sql.Rows
	var err error
	if cursor == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+sqProjectColumns+` FROM projects
			 WHERE tenant_id = ? AND archived = 0 AND `+acl+`
			 ORDER BY created_at DESC LIMIT ?`,
			tenantID, userID, userID, userID, pageSize+1)
	} else {
		before := db.FormatTime(db.ParseTime(cursor))
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+sqProjectColumns+` FROM projects
			 WHERE tenant_id = ? AND archived = 0 AND `+acl+` AND created_at < ?
			 ORDER BY created_at DESC LIMIT ?`,
			tenantID, userID, userID, userID, before, pageSize+1)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := make([]*Project, 0, pageSize)
	for rows.Next() {
		p, err := sqScanProject(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > pageSize {
		next = out[pageSize-1].CreatedAt.Format(time.RFC3339Nano)
		out = out[:pageSize]
	}
	return out, next, nil
}

func (s *SQLiteStore) ArchiveProject(ctx context.Context, tenantID, userID, id string) (*Project, error) {
	now := sqNow()
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET archived = 1, updated_at = ?
		 WHERE id = ? AND tenant_id = ?
		   AND (? = '' OR owner_user = ? OR EXISTS (
		     SELECT 1 FROM project_collaborators pc
		      WHERE pc.project_id = projects.id AND pc.user_id = ?))`,
		now, id, tenantID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrProjectNotFound
	}
	return s.GetProject(ctx, tenantID, userID, id)
}

// ─── 源版本 ──────────────────────────────────────────────────────────────────

func (s *SQLiteStore) CreateSourceRevision(ctx context.Context, tenantID string, in NewSourceRevision) (*SourceRevision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var current int
	if err := tx.QueryRowContext(ctx,
		`SELECT current_revision FROM projects WHERE id = ? AND tenant_id = ?`,
		in.ProjectID, tenantID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}

	// 上传链路幂等：同一 upload_id 已建源版本时返回既有版本。
	if in.UploadID != "" {
		existing, err := sqScanSourceRevision(tx.QueryRowContext(ctx,
			`SELECT `+sqSourceRevisionColumns+` FROM source_revisions
			 WHERE tenant_id = ? AND upload_id = ?`, tenantID, in.UploadID))
		switch {
		case err == nil:
			return existing, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}

	sr := &SourceRevision{
		ProjectID: in.ProjectID, TenantID: tenantID, RevisionNo: current + 1,
		SourceHash: in.SourceHash, ObjectKey: in.ObjectKey, ParserVersion: in.ParserVersion,
		UploadID: in.UploadID,
	}
	sr.ID = uuid.New().String()
	now := sqNow()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO source_revisions (id, project_id, tenant_id, revision_no, source_hash,
		   object_key, parser_version, upload_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sr.ID, sr.ProjectID, sr.TenantID, sr.RevisionNo, sr.SourceHash, sr.ObjectKey,
		sr.ParserVersion, sr.UploadID, now); err != nil {
		return nil, err
	}
	sr.CreatedAt = db.ParseTime(now)
	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET current_revision = ?, updated_at = ? WHERE id = ? AND tenant_id = ?`,
		sr.RevisionNo, now, in.ProjectID, tenantID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sr, nil
}

func (s *SQLiteStore) GetSourceRevision(ctx context.Context, tenantID, projectID string, revisionNo int) (*SourceRevision, error) {
	r, err := sqScanSourceRevision(s.db.QueryRowContext(ctx,
		`SELECT `+sqSourceRevisionColumns+` FROM source_revisions
		 WHERE project_id = ? AND tenant_id = ? AND revision_no = ?`,
		projectID, tenantID, revisionNo))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSourceRevisionNotFound
	}
	return r, err
}

func (s *SQLiteStore) ListSourceRevisions(ctx context.Context, tenantID, projectID string) ([]*SourceRevision, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqSourceRevisionColumns+` FROM source_revisions
		 WHERE project_id = ? AND tenant_id = ? AND source_deleted_at IS NULL
		 ORDER BY revision_no DESC`, projectID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SourceRevision
	for rows.Next() {
		r, err := sqScanSourceRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─── 标签 ────────────────────────────────────────────────────────────────────

func (s *SQLiteStore) ListTags(ctx context.Context, tenantID string) ([]*Tag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(color, ''), created_at FROM tags
		 WHERE tenant_id = ? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tag
	for rows.Next() {
		var t Tag
		var created string
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = db.ParseTime(created)
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CreateTag(ctx context.Context, tenantID, name, color string) (*Tag, error) {
	name = strings.TrimSpace(name)
	id := uuid.New().String()
	now := sqNow()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tags (id, tenant_id, name, color, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, tenantID, name, sqNullIfEmpty(color), now); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTagNameExists
		}
		return nil, err
	}
	return &Tag{ID: id, TenantID: tenantID, Name: name, Color: color, CreatedAt: db.ParseTime(now)}, nil
}

func (s *SQLiteStore) RenameTag(ctx context.Context, tenantID, tagID, name, color string) (*Tag, error) {
	name = strings.TrimSpace(name)
	res, err := s.db.ExecContext(ctx,
		`UPDATE tags SET name = ?, color = ? WHERE id = ? AND tenant_id = ?`,
		name, sqNullIfEmpty(color), tagID, tenantID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTagNameExists
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrTagNotFound
	}
	var t Tag
	var created string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(color, ''), created_at FROM tags WHERE id = ?`,
		tagID).Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &created); err != nil {
		return nil, err
	}
	t.CreatedAt = db.ParseTime(created)
	return &t, nil
}

func (s *SQLiteStore) DeleteTag(ctx context.Context, tenantID, tagID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tags WHERE id = ? AND tenant_id = ?`, tagID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTagNotFound
	}
	return nil
}

func (s *SQLiteStore) AttachTag(ctx context.Context, tenantID, projectID, tagID string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_tags (tenant_id, project_id, tag_id) VALUES (?, ?, ?)
		 ON CONFLICT(project_id, tag_id) DO NOTHING`, tenantID, projectID, tagID); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) DetachTag(ctx context.Context, tenantID, projectID, tagID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_tags WHERE tenant_id = ? AND project_id = ? AND tag_id = ?`,
		tenantID, projectID, tagID)
	return err
}

// ─── 分组 ────────────────────────────────────────────────────────────────────

func (s *SQLiteStore) ListFolders(ctx context.Context, tenantID string) ([]*Folder, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(created_by, ''), created_at FROM folders
		 WHERE tenant_id = ? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Folder
	for rows.Next() {
		var f Folder
		var created string
		if err := rows.Scan(&f.ID, &f.TenantID, &f.Name, &f.CreatedBy, &created); err != nil {
			return nil, err
		}
		f.CreatedAt = db.ParseTime(created)
		out = append(out, &f)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CreateFolder(ctx context.Context, tenantID, name, createdBy string) (*Folder, error) {
	name = strings.TrimSpace(name)
	id := uuid.New().String()
	now := sqNow()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO folders (id, tenant_id, name, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, tenantID, name, sqNullIfEmpty(createdBy), now); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrFolderNameExists
		}
		return nil, err
	}
	return &Folder{ID: id, TenantID: tenantID, Name: name, CreatedBy: createdBy, CreatedAt: db.ParseTime(now)}, nil
}

func (s *SQLiteStore) RenameFolder(ctx context.Context, tenantID, folderID, name string) (*Folder, error) {
	name = strings.TrimSpace(name)
	res, err := s.db.ExecContext(ctx,
		`UPDATE folders SET name = ? WHERE id = ? AND tenant_id = ?`, name, folderID, tenantID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrFolderNotFound
	}
	var f Folder
	var created string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(created_by, ''), created_at FROM folders WHERE id = ?`,
		folderID).Scan(&f.ID, &f.TenantID, &f.Name, &f.CreatedBy, &created); err != nil {
		return nil, err
	}
	f.CreatedAt = db.ParseTime(created)
	return &f, nil
}

func (s *SQLiteStore) DeleteFolder(ctx context.Context, tenantID, folderID string) error {
	// 分组下有项目时禁止删除。
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE tenant_id = ? AND folder_id = ?`, tenantID, folderID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrFolderNotEmpty
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM folders WHERE id = ? AND tenant_id = ?`, folderID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFolderNotFound
	}
	return nil
}

func (s *SQLiteStore) MoveProject(ctx context.Context, tenantID, projectID, folderID string) error {
	var folder any
	if folderID != "" {
		folder = folderID
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET folder_id = ?, updated_at = ? WHERE id = ? AND tenant_id = ?`,
		folder, sqNow(), projectID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

func (s *SQLiteStore) ListProjectOrganization(ctx context.Context, tenantID, userID string) ([]*ProjectOrg, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, COALESCE(p.folder_id, ''),
		        COALESCE((SELECT group_concat(t.id, ',') FROM project_tags pt
		                   JOIN tags t ON t.id = pt.tag_id AND t.tenant_id = p.tenant_id
		                  WHERE pt.project_id = p.id), '')
		 FROM projects p
		 WHERE p.tenant_id = ?
		   AND (? = '' OR p.owner_user = ? OR EXISTS (
		     SELECT 1 FROM project_collaborators pc
		      WHERE pc.project_id = p.id AND pc.user_id = ?))`,
		tenantID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProjectOrg
	for rows.Next() {
		var o ProjectOrg
		var tagIDs string
		if err := rows.Scan(&o.ProjectID, &o.FolderID, &tagIDs); err != nil {
			return nil, err
		}
		if tagIDs != "" {
			o.TagIDs = strings.Split(tagIDs, ",")
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

// ─── 协作者 ──────────────────────────────────────────────────────────────────

func (s *SQLiteStore) ListCollaborators(ctx context.Context, tenantID, projectID string) ([]*Collaborator, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.project_id, c.user_id, c.role, COALESCE(c.invited_by, ''), c.created_at, c.last_accessed_at,
		        COALESCE(u.email, ''), COALESCE(p.username, ''), COALESCE(p.full_name, '')
		 FROM project_collaborators c
		 LEFT JOIN users u ON u.id = c.user_id
		 LEFT JOIN user_profiles p ON p.user_id = c.user_id
		 WHERE c.tenant_id = ? AND c.project_id = ?
		 ORDER BY c.created_at`, tenantID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Collaborator
	for rows.Next() {
		var c Collaborator
		var created string
		var lastAccessed sql.NullString
		if err := rows.Scan(&c.ProjectID, &c.UserID, &c.Role, &c.InvitedBy, &created, &lastAccessed,
			&c.Email, &c.Username, &c.FullName); err != nil {
			return nil, err
		}
		c.CreatedAt = db.ParseTime(created)
		c.LastAccessedAt = db.ParseNullTime(lastAccessed)
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) InviteCollaborator(ctx context.Context, tenantID, projectID, userID, role, invitedBy string) (*Collaborator, error) {
	if !ValidCollaboratorRole(role) {
		return nil, ErrInvalidCollaboratorRole
	}
	now := sqNow()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_collaborators (tenant_id, project_id, user_id, role, invited_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, user_id) DO UPDATE SET role = excluded.role`,
		tenantID, projectID, userID, role, sqNullIfEmpty(invitedBy), now); err != nil {
		return nil, err
	}
	return &Collaborator{ProjectID: projectID, UserID: userID, Role: role, InvitedBy: invitedBy,
		CreatedAt: db.ParseTime(now)}, nil
}

func (s *SQLiteStore) UpdateCollaboratorRole(ctx context.Context, tenantID, projectID, userID, role string) (*Collaborator, error) {
	if !ValidCollaboratorRole(role) {
		return nil, ErrInvalidCollaboratorRole
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE project_collaborators SET role = ?
		 WHERE tenant_id = ? AND project_id = ? AND user_id = ?`,
		role, tenantID, projectID, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrCollaboratorNotFound
	}
	return &Collaborator{ProjectID: projectID, UserID: userID, Role: role}, nil
}

func (s *SQLiteStore) RemoveCollaborator(ctx context.Context, tenantID, projectID, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM project_collaborators WHERE tenant_id = ? AND project_id = ? AND user_id = ?`,
		tenantID, projectID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCollaboratorNotFound
	}
	return nil
}

// ─── 分享链接 ────────────────────────────────────────────────────────────────

const sqShareLinkColumns = `id, tenant_id, project_id, token, access_mode, password_protected,
	password_hash, expires_at, revoked, created_by, created_at, last_accessed_at`

func sqScanShareLink(row rowScanner) (*ShareLink, error) {
	var l ShareLink
	var protected, revoked int
	var createdAt string
	var expiresAt, lastAccessed, passwordHash, createdBy sql.NullString
	if err := row.Scan(&l.ID, &l.TenantID, &l.ProjectID, &l.Token, &l.AccessMode, &protected,
		&passwordHash, &expiresAt, &revoked, &createdBy, &createdAt, &lastAccessed); err != nil {
		return nil, err
	}
	l.PasswordProtected = protected != 0
	l.Revoked = revoked != 0
	l.PasswordHash = passwordHash.String
	l.CreatedBy = createdBy.String
	l.CreatedAt = db.ParseTime(createdAt)
	l.ExpiresAt = db.ParseNullTime(expiresAt)
	l.LastAccessedAt = db.ParseNullTime(lastAccessed)
	return &l, nil
}

func (s *SQLiteStore) CreateShareLink(ctx context.Context, tenantID, projectID, accessMode, passwordHash, createdBy string, expiresAt *time.Time) (*ShareLink, error) {
	if !ValidShareAccessMode(accessMode) {
		return nil, ErrShareLinkNotFound
	}
	id := uuid.New().String()
	token := strings.ReplaceAll(uuid.New().String(), "-", "")
	now := sqNow()
	protected := 0
	var hash any
	if passwordHash != "" {
		protected = 1
		hash = passwordHash
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_share_links (id, tenant_id, project_id, token, access_mode,
		   password_hash, password_protected, expires_at, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tenantID, projectID, token, accessMode, hash, protected, db.FormatTimePtr(expiresAt),
		sqNullIfEmpty(createdBy), now); err != nil {
		return nil, err
	}
	return &ShareLink{ID: id, TenantID: tenantID, ProjectID: projectID, Token: token,
		AccessMode: accessMode, PasswordProtected: protected == 1, ExpiresAt: expiresAt,
		CreatedBy: createdBy, CreatedAt: db.ParseTime(now)}, nil
}

func (s *SQLiteStore) ListShareLinks(ctx context.Context, tenantID, projectID string) ([]*ShareLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqShareLinkColumns+` FROM project_share_links
		 WHERE tenant_id = ? AND project_id = ? ORDER BY created_at DESC`, tenantID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ShareLink
	for rows.Next() {
		l, err := sqScanShareLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) RevokeShareLink(ctx context.Context, tenantID, linkID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE project_share_links SET revoked = 1 WHERE id = ? AND tenant_id = ?`, linkID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrShareLinkNotFound
	}
	return nil
}

// GetShareLinkByToken 匿名按 token 取链接：单租户无 RLS，直接按"未撤回且未过期"过滤。
func (s *SQLiteStore) GetShareLinkByToken(ctx context.Context, token string) (*ShareLink, error) {
	l, err := sqScanShareLink(s.db.QueryRowContext(ctx,
		`SELECT `+sqShareLinkColumns+` FROM project_share_links
		 WHERE token = ? AND revoked = 0
		   AND (expires_at IS NULL OR expires_at > ?)`,
		token, sqNow()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShareLinkNotFound
	}
	return l, err
}

func (s *SQLiteStore) ShareLinkPasswordHash(ctx context.Context, tenantID, linkID string) (string, error) {
	var hash sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT password_hash FROM project_share_links WHERE id = ? AND tenant_id = ?`,
		linkID, tenantID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrShareLinkNotFound
	}
	return hash.String, err
}

func (s *SQLiteStore) TouchShareLinkAccess(ctx context.Context, tenantID, linkID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE project_share_links SET last_accessed_at = ? WHERE id = ? AND tenant_id = ?`,
		sqNow(), linkID, tenantID)
	return err
}

// ─── 内部辅助 ────────────────────────────────────────────────────────────────

func sqNullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueViolation 判断 SQLite 唯一约束冲突（modernc 返回错误串含 "UNIQUE constraint failed"）。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
