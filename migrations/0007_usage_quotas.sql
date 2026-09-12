-- ppts 配额与用量账本（0007，2026-09-12；G3-2）
-- 目标（V4.0 §12.2）：额度"预占 → 执行 → 结算/释放"；并发下原子条件更新；
-- 供应商成本与用户计费分离（usage_ledger 记录实际量，price_version 记录定价版本）。
-- 设计：tenant_quotas 为每租户每计量种类的额度行；quota_reservations 记录单次逻辑操作的预占，
-- 以 (tenant_id, logical_operation_id, usage_kind) 唯一保证幂等。

CREATE TABLE IF NOT EXISTS tenant_quotas (
    tenant_id      uuid NOT NULL REFERENCES tenants(id),
    usage_kind     text NOT NULL DEFAULT 'gen_seconds',
    limit_units    numeric NOT NULL DEFAULT -1, -- < 0 表示不限量
    reserved_units numeric NOT NULL DEFAULT 0,
    consumed_units numeric NOT NULL DEFAULT 0,
    price_version  text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, usage_kind)
);

CREATE TABLE IF NOT EXISTS quota_reservations (
    id                   uuid PRIMARY KEY,
    tenant_id            uuid NOT NULL,
    logical_operation_id text NOT NULL,
    usage_kind           text NOT NULL,
    reserved_units       numeric NOT NULL,
    state                text NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','settled','released')),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, logical_operation_id, usage_kind)
);
CREATE INDEX IF NOT EXISTS idx_quota_reservations_tenant ON quota_reservations(tenant_id, state, created_at);

-- RLS 纵深：新表同样启用 FORCE RLS 与租户策略（V4.0 §12.1）。
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['tenant_quotas', 'quota_reservations'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I '
            'USING (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::uuid) '
            'WITH CHECK (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::uuid)',
            t);
    END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ppts_app;
