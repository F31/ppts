package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 cgo），驱动名 "sqlite"
)

// sqliteSchemaMigrations 与 PG 使用同名跟踪表，便于两套迁移器语义一致。
const sqliteSchemaMigrations = `CREATE TABLE IF NOT EXISTS ppts_schema_migrations (
	version    TEXT PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`

// OpenSQLite 打开 SQLite 数据库并设置连接级 PRAGMA。
//
// dsn：
//   - 文件路径（如 ~/.ppts/ppts.db）；
//   - ":memory:"（单连接共享内存，主要用于测试）；
//   - 已带 file: 前缀的 URI。
//
// PRAGMA 经 DSN 注入（per-connection，连接池下每条连接都生效）：
// foreign_keys=ON、busy_timeout=5s、journal_mode=WAL（内存库除外）、synchronous=NORMAL。
func OpenSQLite(ctx context.Context, dsn string) (*sql.DB, error) {
	full := sqliteDSN(dsn)
	sqldb, err := sql.Open("sqlite", full)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite: %w", err)
	}
	// SQLite 单写者：限制连接数避免写锁竞争（WAL 下读并发仍可）。
	sqldb.SetMaxOpenConns(8)
	sqldb.SetMaxIdleConns(4)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: ping sqlite: %w", err)
	}
	return sqldb, nil
}

func sqliteDSN(dsn string) string {
	pragmas := []string{
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
	}
	switch {
	case dsn == ":memory:" || strings.HasPrefix(dsn, "file::memory:"):
		pragmas = append(pragmas, "_pragma=journal_mode(MEMORY)")
		return "file::memory:?cache=shared&" + strings.Join(pragmas, "&")
	case strings.HasPrefix(dsn, "file:"):
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + strings.Join(pragmas, "&") + "&_pragma=journal_mode(WAL)"
	default:
		return "file:" + dsn + "?" + strings.Join(pragmas, "&") + "&_pragma=journal_mode(WAL)"
	}
}

// MigrateSQLite 按文件名序应用 SQLite 迁移，跟踪记录落在 ppts_schema_migrations（幂等）。
func MigrateSQLite(ctx context.Context, sqldb *sql.DB, fsys fs.FS) ([]string, error) {
	if _, err := sqldb.ExecContext(ctx, sqliteSchemaMigrations); err != nil {
		return nil, fmt.Errorf("db: create sqlite tracking table: %w", err)
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("db: list sqlite migrations: %w", err)
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		var exists int
		if err := sqldb.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM ppts_schema_migrations WHERE version = ?`, name).Scan(&exists); err != nil {
			return applied, fmt.Errorf("db: check %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return applied, fmt.Errorf("db: read %s: %w", name, err)
		}
		tx, err := sqldb.BeginTx(ctx, nil)
		if err != nil {
			return applied, fmt.Errorf("db: begin %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("db: apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ppts_schema_migrations (version) VALUES (?)`, name); err != nil {
			_ = tx.Rollback()
			return applied, fmt.Errorf("db: record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return applied, fmt.Errorf("db: commit %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

// EnsureLocalIdentity 播种单租户 profile 的固定租户/用户/成员（幂等）。
// SQLite 模式无登录，所有请求以 (LocalTenantID, LocalUserID) 身份运行。
func EnsureLocalIdentity(ctx context.Context, sqldb *sql.DB) error {
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`,
			[]any{LocalTenantID, LocalTenantName}},
		{`INSERT INTO users (id, email) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`,
			[]any{LocalUserID, LocalUserEmail}},
		{`INSERT INTO tenant_members (tenant_id, user_id, role) VALUES (?, ?, 'owner')
		  ON CONFLICT(tenant_id, user_id) DO NOTHING`,
			[]any{LocalTenantID, LocalUserID}},
	}
	for _, s := range stmts {
		if _, err := sqldb.ExecContext(ctx, s.sql, s.args...); err != nil {
			return fmt.Errorf("db: ensure local identity: %w", err)
		}
	}
	return nil
}

// ─── 时间戳编解码（SQLite 统一 RFC3339 毫秒 UTC）────────────────────────────────

// TimeFormat 是 SQLite 时间列的存储格式（RFC3339 毫秒 UTC，可被 time.RFC3339Nano 解析）。
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Now 返回当前时间的 SQLite 存储字符串（UTC）。
func Now() string { return FormatTime(time.Now()) }

// FormatTime 将 time.Time 转为 SQLite 存储字符串（UTC）。
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// FormatTimePtr 处理可空时间列。
func FormatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

// ParseTime 解析 SQLite 时间列；空串/非法值返回零值。
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{TimeFormat, time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ParseNullTime 处理可空时间列。
func ParseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := ParseTime(ns.String)
	return &t
}
