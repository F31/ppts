package artifact

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
)

// sqShareLinkColumns 是 SQLite 侧单一列清单，顺序必须与 pg 的 shareLinkColumns 完全一致，
// 也与 sqScanShareLink 的 Scan 目标顺序相邻可读。
//
// ⚠️ 本包踩过这个坑：列清单与 Scan 目标错位在编译期完全不可见，只会在运行时
// 把值读进错误字段（甚至静默读错值）。规则同 sqArtifactColumns：改一处必改另一处，
// 由 share_link_test.go 的往返用例兜底。
const sqShareLinkColumns = `id, tenant_id, artifact_id, project_id, token, access_mode,
	password_protected, expires_at, revoked, created_by, created_at, last_accessed_at`

func sqScanShareLink(row rowScanner) (*ShareLink, error) {
	var l ShareLink
	var protected, revoked int
	var createdAt string
	var expires, lastAccessed, createdBy sql.NullString
	if err := row.Scan(&l.ID, &l.TenantID, &l.ArtifactID, &l.ProjectID, &l.Token, &l.AccessMode,
		&protected, &expires, &revoked, &createdBy, &createdAt, &lastAccessed); err != nil {
		return nil, err
	}
	l.PasswordProtected = protected != 0
	l.Revoked = revoked != 0
	l.ExpiresAt = db.ParseNullTime(expires)
	l.LastAccessedAt = db.ParseNullTime(lastAccessed)
	l.CreatedBy = createdBy.String
	l.CreatedAt = db.ParseTime(createdAt)
	return &l, nil
}

func (s *SQLiteStore) CreateShareLink(ctx context.Context, tenantID string, in NewShareLink) (*ShareLink, error) {
	token, err := newShareToken()
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO artifact_share_links (id, tenant_id, artifact_id, project_id, token,
		   access_mode, password_hash, password_protected, expires_at, revoked, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id, tenantID, in.ArtifactID, in.ProjectID, token, normalizeShareAccessMode(in.AccessMode),
		nullString(in.PasswordHash), in.PasswordHash != "", db.FormatTimePtr(in.ExpiresAt),
		nullString(in.CreatedBy), db.Now()); err != nil {
		return nil, err
	}
	return sqScanShareLink(s.db.QueryRowContext(ctx,
		`SELECT `+sqShareLinkColumns+` FROM artifact_share_links WHERE id = ? AND tenant_id = ?`,
		id, tenantID))
}

func (s *SQLiteStore) ListShareLinks(ctx context.Context, tenantID, artifactID string) ([]*ShareLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqShareLinkColumns+` FROM artifact_share_links
		 WHERE artifact_id = ? AND tenant_id = ? ORDER BY created_at DESC`, artifactID, tenantID)
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
		`UPDATE artifact_share_links SET revoked = 1 WHERE id = ? AND tenant_id = ?`, linkID, tenantID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrShareLinkNotFound
	}
	return nil
}

// GetShareLinkByToken 的有效性判据与 PG 侧同义：SQLite 没有 anon_read 策略兜底，
// 所以这里的 WHERE 就是唯一防线（见 sqShareLinkValid 的注释）。
func (s *SQLiteStore) GetShareLinkByToken(ctx context.Context, token string) (*ShareLink, error) {
	l, err := sqScanShareLink(s.db.QueryRowContext(ctx,
		`SELECT `+sqShareLinkColumns+` FROM artifact_share_links WHERE token = ? AND `+sqShareLinkValid,
		token, db.Now()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShareLinkNotFound
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (s *SQLiteStore) ShareLinkPasswordHash(ctx context.Context, tenantID, linkID string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(password_hash,'') FROM artifact_share_links WHERE id = ? AND tenant_id = ?`,
		linkID, tenantID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrShareLinkNotFound
	}
	return hash, err
}

func (s *SQLiteStore) TouchShareLinkAccess(ctx context.Context, tenantID, linkID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE artifact_share_links SET last_accessed_at = ? WHERE id = ? AND tenant_id = ?`,
		db.Now(), linkID, tenantID)
	return err
}
