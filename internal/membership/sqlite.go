package membership

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile）。
// 表结构保留 tenant_members / user_profiles，本地用户为固定 owner（由 db.EnsureLocalIdentity 播种）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 成员存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

func (s *SQLiteStore) GetRole(ctx context.Context, tenantID, userID string) (Role, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM tenant_members WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return Role(role), nil
}

func (s *SQLiteStore) List(ctx context.Context, tenantID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tm.user_id, tm.role, tm.created_at,
		        COALESCE(u.email, ''),
		        COALESCE(p.username, ''), COALESCE(p.full_name, ''),
		        COALESCE(p.gender, ''), COALESCE(p.birth_date, ''), COALESCE(p.phone, '')
		 FROM tenant_members tm
		 LEFT JOIN users u ON u.id = tm.user_id
		 LEFT JOIN user_profiles p ON p.user_id = tm.user_id
		 WHERE tm.tenant_id = ?
		 ORDER BY tm.created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var createdAt string
		if err := rows.Scan(&m.UserID, (*string)(&m.Role), &createdAt,
			&m.Email, &m.Username, &m.FullName, &m.Gender, &m.BirthDate, &m.Phone); err != nil {
			return nil, err
		}
		m.CreatedAt = db.ParseTime(createdAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SetRole(ctx context.Context, tenantID, userID string, role Role) error {
	tenantID = strings.TrimSpace(tenantID)
	userID = strings.TrimSpace(userID)
	if tenantID == "" || userID == "" {
		return errors.New("membership: tenant_id and user_id are required")
	}
	if !Valid(role) {
		return errors.New("membership: invalid role")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tenant_members (tenant_id, user_id, role, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(tenant_id, user_id) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`,
		tenantID, userID, string(role), db.Now(), db.Now())
	return err
}

func (s *SQLiteStore) Remove(ctx context.Context, tenantID, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM tenant_members WHERE tenant_id = ? AND user_id = ?`, tenantID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SaveProfile 写入成员档案（user_profiles 无租户列，按 user_id 定位）。
func (s *SQLiteStore) SaveProfile(ctx context.Context, userID string, p Profile) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("membership: user_id is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_profiles (user_id, username, full_name, gender, birth_date, phone, created_at, updated_at)
		 VALUES (?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)
		 ON CONFLICT(user_id) DO UPDATE
		   SET username = excluded.username, full_name = excluded.full_name,
		       gender = excluded.gender, birth_date = excluded.birth_date,
		       phone = excluded.phone, updated_at = excluded.updated_at`,
		userID, p.Username, p.FullName, p.Gender, p.BirthDate, p.Phone, db.Now(), db.Now())
	return err
}
