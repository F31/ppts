-- ppts 分布式限流计数（0047，2026-09-27；P2-B1）
--
-- 背景：认证端点的限流计数此前全部在进程内存里（internal/api/ratelimit.go 的
-- fixedWindowLimiter）。单副本下正确；一旦横向扩副本，实际放行量 = 单副本限额 × 副本数，
-- 撞库/批量注册的防护被线性稀释 —— 而扩副本正是承受攻击时的第一反应。
--
-- 建模：
--   - 主键 (scope, bucket_key)：scope 区分端点（register/login/forgot/resend…），
--     bucket_key 是限流维度（客户端 IP）。一个 key 只占一行，靠 UPSERT 原地累加；
--   - window_start 存当前窗口起点（由 Go 侧对 window 长度做 epoch 截断，实例间一致）。
--     窗口切换靠 window_start 比较判定，不需要后台任务清理旧窗口；
--   - count 单调累加（超限后仍继续计数，便于事后看攻击强度），帽子由 Go 侧传入，
--     避免撞库时把单行撑到无界。
--
-- 隔离说明（重要）：本表**不设 RLS**。它是按客户端 IP 计数的全局基础设施表，
-- 与租户无关 —— 若套 tenant_id 策略，限流行将无法写入（app.tenant_id 在认证链路上
-- 尚未确定，认证端点恰恰是在"还没有租户"时才被访问的）。访问面靠授权收敛：
-- 只授予 ppts_app，不授予 PUBLIC，且没有任何端点直接暴露该表。
CREATE TABLE IF NOT EXISTS ppts_rate_limit (
    scope        text NOT NULL,
    bucket_key   text NOT NULL,
    window_start timestamptz NOT NULL,
    count        bigint NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, bucket_key)
);

-- 过期窗口清理按 updated_at 扫（窗口起点本身不足以判断"多久没被访问"）。
CREATE INDEX IF NOT EXISTS idx_rate_limit_updated ON ppts_rate_limit (updated_at);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE ppts_rate_limit OWNER TO ppts_migrator;
    END IF;
END $$;

REVOKE ALL ON ppts_rate_limit FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE, DELETE ON ppts_rate_limit TO ppts_app;

DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON ppts_rate_limit TO ppts_app;
    END IF;
END $$;
