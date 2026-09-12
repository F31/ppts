package tenant

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run 在一个事务内设置事务局部的 app.tenant_id（set_config 第三参数 true），
// 然后执行 fn；提交或回滚时上下文自动清除，避免连接池复用时串租户（V4.0 §12.1）。
//
// RLS 策略使用 NULLIF(current_setting('app.tenant_id', true), ”)::uuid：
// tenantID 为空时策略结果为 NULL，所有行列被拒绝（"上下文缺失即拒绝"）。
func Run(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		_ = tx.Rollback(context.Background())
		return err
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback(context.Background())
		return err
	}
	return tx.Commit(context.Background())
}

// RunCtx 使用 context 中的租户执行 Run；context 缺失租户时仍以空值执行，
// 由 RLS 拒绝访问（不静默放行）。
func RunCtx(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context, pgx.Tx) error) error {
	tenantID, _ := FromContext(ctx)
	return Run(ctx, pool, tenantID, fn)
}
