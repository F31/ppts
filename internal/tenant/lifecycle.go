package tenant

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/google/uuid"
)

// Status 是租户生命周期状态。
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

// Status 返回租户生命周期状态；不存在返回 ErrTenantNotFound。
func (s *PGStore) Status(ctx context.Context, tenantID string) (Status, error) {
	var status string
	err := s.pool.QueryRow(ctx, "SELECT status FROM tenants WHERE id=$1", tenantID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTenantNotFound
	}
	if err != nil {
		return "", err
	}
	return Status(status), nil
}

// TenantActive 供 API 身份中间件检查租户是否可服务。
func (s *PGStore) TenantActive(ctx context.Context, tenantID string) (bool, error) {
	status, err := s.Status(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return status == StatusActive, nil
}

// Suspend 停用租户。
func (s *PGStore) Suspend(ctx context.Context, tenantID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE tenants SET status='suspended', suspended_at=now(), updated_at=now() WHERE id=$1", tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// Resume 恢复租户。
func (s *PGStore) Resume(ctx context.Context, tenantID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE tenants SET status='active', suspended_at=NULL, updated_at=now() WHERE id=$1", tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// Summary 是运营商后台所需的租户概览行。
type Summary struct {
	ID        string
	Name      string
	Type      string
	Status    Status
	CreatedAt time.Time
}

// ListTenants 返回租户概览（按创建时间倒序，最多 limit 条）。为兼容旧调用保留；
// 新代码请使用 ListTenantsPage 获取分页与 nextCursor。
func (s *PGStore) ListTenants(ctx context.Context, limit int) ([]Summary, error) {
	rows, _, err := s.ListTenantsPage(ctx, "", limit)
	return rows, err
}

// ListTenantsPage 返回一页租户概览（按 created_at DESC, id DESC 排序的 keyset 游标分页）。
//
// 游标（cursor）是不透明字符串：编码「上一页最后一条的 (created_at, id)」，用于
// WHERE (created_at, id) < 上一页最后一条 的稳定翻页（同刻创建的租户不漏页、无 OFFSET
// 漂移）。空 cursor 表示第一页。返回 (本页行, 下一页游标, error)；nextCursor 为空表示
// 已到最后一页。
func (s *PGStore) ListTenantsPage(ctx context.Context, cursor string, pageSize int) ([]Summary, string, error) {
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 200
	}
	q := `SELECT id::text, name, type, status, created_at FROM tenants`
	args := []any{}
	if cursor != "" {
		created, id, ok := decodeTenantCursor(cursor)
		if !ok {
			// 非法游标：按第一页处理（不返回 500）。
			cursor = ""
		} else {
			args = append(args, created, id)
			q += ` WHERE (created_at, id) < ($1::timestamptz, $2::uuid)`
		}
	}
	// 多取一条判断是否还有下一页。
	q += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, pageSize+1)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var sm Summary
		var status string
		if err := rows.Scan(&sm.ID, &sm.Name, &sm.Type, &status, &sm.CreatedAt); err != nil {
			return nil, "", err
		}
		sm.Status = Status(status)
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > pageSize {
		next = encodeTenantCursor(out[pageSize-1].CreatedAt, out[pageSize-1].ID)
		out = out[:pageSize]
	}
	return out, next, nil
}

// CountTenants 返回租户总数（运营后台分页指示用）。
func (s *PGStore) CountTenants(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tenants`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// tenantCursorRawLen 是游标载荷的字段布局版本号。
const tenantCursorVer = "v1"

// encodeTenantCursor 编码 (created_at, id) 为 URL-safe 不透明字符串。
func encodeTenantCursor(created time.Time, id string) string {
	payload := tenantCursorVer + "|" + created.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// decodeTenantCursor 解码游标；格式非法返回 ok=false（调用方按第一页处理）。
func decodeTenantCursor(cursor string) (time.Time, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 || parts[0] != tenantCursorVer {
		return time.Time{}, "", false
	}
	created, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return time.Time{}, "", false
	}
	if _, err := uuid.Parse(parts[2]); err != nil {
		return time.Time{}, "", false
	}
	return created, parts[2], true
}
