-- ppts 审计日志（0009，2026-09-12；G3-4）
-- 目标（V4.0 §12.3/§13）：可破坏性/敏感操作按租户可检索、可归属到操作者。
-- 设计：audit_events 仅追加；metadata 保存操作上下文；与业务同租户事务写入（受 RLS 约束）。

CREATE TABLE IF NOT EXISTS audit_events (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id),
    actor_user    text NOT NULL DEFAULT '',
    action        text NOT NULL,
    resource_type text NOT NULL DEFAULT '',
    resource_id   text NOT NULL DEFAULT '',
    metadata      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON audit_events(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_action ON audit_events(tenant_id, action, created_at DESC);

-- RLS 纵深：审计表同样启用 FORCE RLS 与租户策略（V4.0 §12.1）。
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['audit_events'] LOOP
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
