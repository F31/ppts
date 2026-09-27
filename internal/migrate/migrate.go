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
	"github.com/jackc/pgx/v5/pgconn"
)

// migrateLockKey 是迁移互斥锁的固定键值（"PPTS" 的 ASCII）。
//
// 用**会话级** advisory lock（ pg_advisory_lock 而非 pg_advisory_xact_lock ）：
// 锁的生命周期绑定到这条连接，连接一关就自动释放。这样即使进程被 kill -9，
// PostgreSQL 也会在连接断开时清理 —— 不需要任何释放逻辑，也就不存在
// "拿了锁没释放 → 后续所有迁移永久阻塞"这种失败模式。
//
// 为什么需要它：多副本同时启动（滚动发布、扩副本）时会并发应用同一批迁移。
// 虽然每个迁移自带事务，但"检查是否已应用"与"应用"之间存在窗口，两个副本可能
// 同时判定某个迁移未执行并双双执行；DDL 本身幂等性差，结果难以预期。
const migrateLockKey = 0x50505453

// lockWaitSeconds 是等待已有持有者的上限。
//
// 等待是刻意的：迁移幂等，第二个副本等前一个跑完就能安全继续，比直接失败更符合
// 滚动发布的语义。但等待必须有界 —— 无限等待会把"一个实例卡住了"放大成
// "所有副本都卡住且无人报错"。
//
// 超时必须**明确报错**（fail-closed）：让上层启动失败、由编排系统重启重试，
// 远好过在 schema 未完整的情况下放行请求。
const lockWaitSeconds = 60

// lockConn 仅暴露抢锁所需的执行能力，便于在没有数据库的情况下验证语句顺序。
type lockConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// acquireMigrateLock 取得迁移互斥锁。
//
// 关键顺序：先把 statement_timeout 设为等待上限 → 抢锁 → 再把 statement_timeout 恢复。
// 恢复这一步不能省：advisory lock 不受 lock_timeout 约束，只能靠 statement_timeout 限时，
// 而后续真正的迁移语句（建索引、改大表）可能远超 60s，沿用这个超时会被误杀。
// 顺序的三个步骤由 migrate_lock_test.go 的用例守着，包括这一步缺失的情形。
func acquireMigrateLock(ctx context.Context, conn lockConn) error {
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = '%ds'", lockWaitSeconds)); err != nil {
		return fmt.Errorf("migrate: set lock wait timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("migrate: another instance is migrating (waited %ds): %w", lockWaitSeconds, err)
	}
	// 0 = 无限制：迁移本身该跑多久就跑多久。
	if _, err := conn.Exec(ctx, "SET statement_timeout = '0'"); err != nil {
		return fmt.Errorf("migrate: reset statement timeout: %w", err)
	}
	return nil
}

// Apply 连接 dsn 并逐个应用尚未执行的迁移，返回本次实际执行的迁移文件名。
// 已应用的迁移跳过，可反复执行（幂等）。
//
// 整个过程持 migrateLockKey 会话级锁，因此并发调用是安全的。
func Apply(ctx context.Context, dsn string, migrationsFS fs.FS) ([]string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: connect: %w", err)
	}
	defer conn.Close(ctx)

	if err := acquireMigrateLock(ctx, conn); err != nil {
		return nil, err
	}

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
