-- ppts 租户生命周期状态（0011，2026-09-12；G3-4）
-- 目标：支持租户停用/恢复/删除流程的最小控制面状态。
-- tenants 为控制面表，不启用 RLS；运行时请求仍由服务端校验状态后进入租户业务表。

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','suspended','deleted')),
    ADD COLUMN IF NOT EXISTS suspended_at timestamptz,
    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status);

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ppts_app;
