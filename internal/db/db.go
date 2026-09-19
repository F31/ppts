// Package db 提供数据库驱动的选择、连接与迁移分发，使同一二进制通过
// PPTS_DB_DRIVER 在 PostgreSQL（多租户全功能）与 SQLite（单租户轻量）之间切换。
//
// 分层约定（方案 B）：
//   - 领域 Store 接口（internal/<domain>）是稳定契约，不感知驱动；
//   - 各域以 <domain>/postgres.go 与 <domain>/sqlite.go 提供两套实现，由 factory 选择；
//   - PostgreSQL 实现走 pgx + RLS（tenant.Run 设置 app.tenant_id）；
//   - SQLite 实现走 database/sql，单租户，隔离由应用层显式 tenant_id 过滤承担。
package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Driver 标识数据库驱动。
type Driver string

const (
	// DriverSQLite 是单租户轻量模式（默认）：无 RLS/角色/登录，固定本地租户与用户。
	DriverSQLite Driver = "sqlite"
	// DriverPostgres 是完整多租户模式：RLS + SECURITY DEFINER 调度 + 邮箱/OIDC 登录。
	DriverPostgres Driver = "postgres"
)

// 单租户 profile 的固定身份（SQLite 模式）。ID 形状与 PG 的 uuid/text 一致，便于将来迁移数据。
const (
	LocalTenantID    = "00000000-0000-0000-0000-000000000001"
	LocalUserID      = "local-user"
	LocalUserEmail   = "local@localhost"
	LocalTenantName  = "Local"
)

// Config 是数据库连接配置。
type Config struct {
	Driver Driver
	// DSN：postgres 为连接串；sqlite 为文件路径（或 ":memory:" / "file:..."）。
	DSN string
}

// FromEnv 解析 PPTS_DB_DRIVER 与 PPTS_DATABASE_URL。
//
// 规则：
//   - PPTS_DB_DRIVER 未设置时，按 DSN 前缀推断：postgres:// 或 postgresql:// → postgres，其余 → sqlite；
//   - 两者都未设置 → sqlite，默认路径 ~/.ppts/ppts.db（单体开箱即用）；
//   - driver=sqlite 且 DSN 为空 → 默认路径。
func FromEnv() (Config, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("PPTS_DB_DRIVER")))
	dsn := strings.TrimSpace(os.Getenv("PPTS_DATABASE_URL"))

	var d Driver
	switch raw {
	case "":
		if isPostgresDSN(dsn) {
			d = DriverPostgres
		} else {
			d = DriverSQLite
		}
	case string(DriverSQLite):
		d = DriverSQLite
	case string(DriverPostgres), "postgresql":
		d = DriverPostgres
	default:
		return Config{}, fmt.Errorf("db: unknown PPTS_DB_DRIVER %q (want sqlite|postgres)", raw)
	}

	if d == DriverPostgres {
		if dsn == "" {
			return Config{}, errors.New("db: PPTS_DATABASE_URL is required when PPTS_DB_DRIVER=postgres")
		}
		return Config{Driver: d, DSN: dsn}, nil
	}

	// sqlite
	if dsn == "" || isPostgresDSN(dsn) {
		p, err := DefaultSQLitePath()
		if err != nil {
			return Config{}, err
		}
		dsn = p
	}
	return Config{Driver: d, DSN: dsn}, nil
}

func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// DefaultSQLitePath 返回默认 SQLite 数据库路径（~/.ppts/ppts.db），并确保目录存在。
func DefaultSQLitePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("db: resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".ppts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("db: create data dir: %w", err)
	}
	return filepath.Join(dir, "ppts.db"), nil
}
