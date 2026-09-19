package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/F31/ppts/internal/db"
)

// SQLiteStore 是 Store 的 SQLite 实现（单租户精简 profile，无归档）。
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建 SQLite 审计存储。
func NewSQLiteStore(sqldb *sql.DB) *SQLiteStore { return &SQLiteStore{db: sqldb} }

var _ Store = (*SQLiteStore)(nil)

func (s *SQLiteStore) Record(ctx context.Context, e Event) error {
	if strings.TrimSpace(e.TenantID) == "" {
		return ErrTenantRequired
	}
	if strings.TrimSpace(e.Action) == "" {
		return ErrActionRequired
	}
	metadata := e.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO audit_events (id, tenant_id, actor_user, action, resource_type, resource_id, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.New().String(), e.TenantID, e.ActorUser, e.Action, e.ResourceType, e.ResourceID, string(raw), db.Now())
	return err
}

func (s *SQLiteStore) List(ctx context.Context, tenantID string, filter Filter) ([]Event, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{tenantID}
	where := "tenant_id = ?"
	if filter.Action != "" {
		where += " AND action = ?"
		args = append(args, filter.Action)
	}
	if filter.ResourceType != "" {
		where += " AND resource_type = ?"
		args = append(args, filter.ResourceType)
	}
	if !filter.Since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, db.FormatTime(filter.Since))
	}
	if !filter.Before.IsZero() {
		where += " AND created_at < ?"
		args = append(args, db.FormatTime(filter.Before))
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, actor_user, action, resource_type, resource_id, metadata, created_at
		 FROM audit_events WHERE `+where+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := sqScanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func sqScanEvent(row rowScanner) (*Event, error) {
	var e Event
	var raw, created string
	if err := row.Scan(&e.ID, &e.TenantID, &e.ActorUser, &e.Action, &e.ResourceType, &e.ResourceID, &raw, &created); err != nil {
		return nil, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &e.Metadata); err != nil {
			return nil, err
		}
	}
	e.CreatedAt = db.ParseTime(created)
	return &e, nil
}

func (s *SQLiteStore) DeleteBefore(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	if strings.TrimSpace(tenantID) == "" {
		return 0, ErrTenantRequired
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_events WHERE tenant_id = ? AND created_at < ?`, tenantID, db.FormatTime(before))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
