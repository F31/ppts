// Package migrate 按文件名序应用内嵌 SQL 迁移，执行记录落在 ppts_schema_migrations。
//
// 约束与假设：
//   - 迁移内含建角色（0012/0013）与 CREATE EXTENSION（0028），调用方必须用 superuser DSN；
//   - 每个迁移与其跟踪行包在一个事务里（BEGIN…COMMIT 拼进同一条 simple-protocol 批次，
//     因为 pgx 默认 prepared 协议不支持多语句）；
//   - 迁移按文件名排序应用；编号重复的历史文件（0008/0009 各两个）以完整文件名为版本键。
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Apply 连接 dsn 并逐个应用尚未执行的迁移，返回本次实际执行的迁移文件名。
// 已应用的迁移跳过，可反复执行（幂等）。
func Apply(ctx context.Context, dsn string, migrationsFS fs.FS) ([]string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS ppts_schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("migrate: create tracking table: %w", err)
	}

	names, err := fs.Glob(migrationsFS, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrate: list migrations: %w", err)
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM ppts_schema_migrations WHERE version = $1)`,
			name).Scan(&exists); err != nil {
			return applied, fmt.Errorf("migrate: check %s: %w", name, err)
		}
		if exists {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return applied, fmt.Errorf("migrate: read %s: %w", name, err)
		}
		// 版本键来自内嵌文件名，无外部输入；仍做一次引号转义兜底。
		safe := strings.ReplaceAll(name, "'", "''")
		batch := "BEGIN;\n" + string(body) +
			"\nINSERT INTO ppts_schema_migrations (version) VALUES ('" + safe + "');\nCOMMIT;\n"
		// simple protocol：单条消息里多语句整体执行，任一句失败则整个批次报错、事务回滚。
		if _, err := conn.PgConn().Exec(ctx, batch).ReadAll(); err != nil {
			return applied, fmt.Errorf("migrate: apply %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}
