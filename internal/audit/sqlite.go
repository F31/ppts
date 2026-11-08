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
	if ip, ok := ClientIPFromContext(ctx); ok {
		if _, exists := metadata["client_ip"]; !exists {
			metadata["client_ip"] = ip
		}
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

// ViewStats 在 SQLite 侧聚合观看事件（modernc.org/sqlite 自带 JSON1）。
//
// 两条 SQL 分工：第一条给「窗口内总独立会话数」与首末事件时间，第二条按
// metadata.event 分组给每个桶的独立会话数。分组计数不能相加得到总会话数——
// 同一次观看会落 view 与多个 progress 桶，相加会重复计数。
func (s *SQLiteStore) ViewStats(ctx context.Context, tenantID string, q ViewStatsQuery) (ViewStats, error) {
	if err := q.validate(tenantID); err != nil {
		return ViewStats{}, err
	}
	// json_valid 兜住历史行里可能存在的非 JSON metadata：json_extract 遇到坏行会
	// 让整条聚合报错，而看板宁可少算这几行，也不该因为一条脏数据整个打不开。
	const where = `tenant_id = ? AND action = ? AND resource_type = ? AND resource_id = ?
		AND created_at >= ? AND json_valid(metadata)`
	args := []any{tenantID, q.Action, q.ResourceType, q.ResourceID, db.FormatTime(q.Since)}

	out := ViewStats{ByEvent: make(map[string]int64)}
	var first, last sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT json_extract(metadata,'$.session')), MIN(created_at), MAX(created_at)
		 FROM audit_events WHERE `+where, args...).Scan(&out.Sessions, &first, &last); err != nil {
		return ViewStats{}, err
	}
	if first.Valid {
		out.FirstEventAt = db.ParseTime(first.String)
	}
	if last.Valid {
		out.LastEventAt = db.ParseTime(last.String)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(json_extract(metadata,'$.event'),''),
		        COUNT(DISTINCT json_extract(metadata,'$.session'))
		 FROM audit_events WHERE `+where+` GROUP BY 1`, args...)
	if err != nil {
		return ViewStats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return ViewStats{}, err
		}
		out.ByEvent[name] = n
	}
	if err := rows.Err(); err != nil {
		return ViewStats{}, err
	}
	return out, nil
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
