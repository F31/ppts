package artifact

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/F31/ppts/internal/tenant"
)

// shareLinkColumns 是 PG 侧 ShareLink 的单一列清单，顺序即 Scan 顺序。
//
// ⚠️ **不含 password_hash**。匿名读路径共用本清单，把哈希放进来等于让每次匿名
// 请求都把口令哈希读进内存再丢弃——任何一次日志或序列化失误都会把它送到公网侧。
// 比对所需的哈希只经 ShareLinkPasswordHash 在租户上下文里单独取。
const shareLinkColumns = `id, tenant_id, artifact_id, project_id, object_key, content_hash, token,
	access_mode, password_protected, expires_at, revoked, created_by, created_at, last_accessed_at`

func scanShareLink(row rowScanner) (*ShareLink, error) {
	var l ShareLink
	var expires, lastAccessed *time.Time
	var createdBy sql.NullString
	if err := row.Scan(&l.ID, &l.TenantID, &l.ArtifactID, &l.ProjectID, &l.ObjectKey, &l.ContentHash,
		&l.Token, &l.AccessMode, &l.PasswordProtected, &expires, &l.Revoked, &createdBy, &l.CreatedAt, &lastAccessed); err != nil {
		return nil, err
	}
	l.ExpiresAt = expires
	l.CreatedBy = createdBy.String
	l.LastAccessedAt = lastAccessed
	return &l, nil
}

func (s *PGStore) CreateShareLink(ctx context.Context, tenantID string, in NewShareLink) (*ShareLink, error) {
	token, err := newShareToken()
	if err != nil {
		return nil, err
	}
	var out *ShareLink
	err = tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		l, e := scanShareLink(tx.QueryRow(ctx, `INSERT INTO artifact_share_links
			(tenant_id, artifact_id, project_id, object_key, content_hash, token,
			 access_mode, password_hash, password_protected, expires_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING `+shareLinkColumns,
			tenantID, in.ArtifactID, in.ProjectID, in.ObjectKey, in.ContentHash, token,
			normalizeShareAccessMode(in.AccessMode), nullString(in.PasswordHash),
			in.PasswordHash != "", in.ExpiresAt, nullString(in.CreatedBy)))
		out = l
		return e
	})
	return out, err
}

func (s *PGStore) ListShareLinks(ctx context.Context, tenantID, artifactID string) ([]*ShareLink, error) {
	items := []*ShareLink{}
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT `+shareLinkColumns+` FROM artifact_share_links
			WHERE artifact_id=$1 AND tenant_id=$2 ORDER BY created_at DESC`, artifactID, tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			l, sErr := scanShareLink(rows)
			if sErr != nil {
				return sErr
			}
			items = append(items, l)
		}
		return rows.Err()
	})
	return items, err
}

func (s *PGStore) RevokeShareLink(ctx context.Context, tenantID, linkID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, e := tx.Exec(ctx,
			`UPDATE artifact_share_links SET revoked=true WHERE id=$1 AND tenant_id=$2`, linkID, tenantID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return ErrShareLinkNotFound
		}
		return nil
	})
}

// GetShareLinkByToken 走匿名路径：**不带租户上下文**。
//
// 与 project.PGProjectStore.GetShareLinkByToken 同一范式——直连 pool 而不进 tenant.Run，
// 让 tenant_isolation 因子句为 NULL 而拒绝、由 anon_read 策略按「未撤销且未过期」放行。
// 注意 pgx.ErrNoRows 要归一成 ErrShareLinkNotFound：三种失效原因对外必须无法区分。
func (s *PGStore) GetShareLinkByToken(ctx context.Context, token string) (*ShareLink, error) {
	l, err := scanShareLink(s.pool.QueryRow(ctx,
		`SELECT `+shareLinkColumns+` FROM artifact_share_links WHERE token=$1 AND `+pgShareLinkValid, token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShareLinkNotFound
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (s *PGStore) ShareLinkPasswordHash(ctx context.Context, tenantID, linkID string) (string, error) {
	var hash string
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(password_hash,'') FROM artifact_share_links WHERE id=$1 AND tenant_id=$2`,
			linkID, tenantID).Scan(&hash)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrShareLinkNotFound
	}
	return hash, err
}

func (s *PGStore) TouchShareLinkAccess(ctx context.Context, tenantID, linkID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE artifact_share_links SET last_accessed_at=now() WHERE id=$1 AND tenant_id=$2`,
			linkID, tenantID)
		return e
	})
}

// nullString 把空串转成 NULL：created_by / password_hash 的空值语义都是「没有」，
// 存空串会让「有没有设口令」这种判断退化成看字符串长度。
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
