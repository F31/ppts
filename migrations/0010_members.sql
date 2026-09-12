-- ppts 租户成员与角色（0010，2026-09-12；G3-3 成员/角色内核，OIDC 待后续）
-- 目标（V4.0 §12.3）：成员角色 Owner/Admin/Editor/Reviewer/Viewer 持久化，
-- 为授权（取消/重试/导出/费用/成员管理）提供依据。tenant_id 由服务端从身份推导。

CREATE TABLE IF NOT EXISTS tenant_members (
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    text NOT NULL,
    role       text NOT NULL DEFAULT 'viewer' CHECK (role IN ('owner','admin','editor','reviewer','viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_tenant_members_tenant ON tenant_members(tenant_id, role);

-- RLS 纵深：成员表同样启用 FORCE RLS 与租户策略（V4.0 §12.1）。
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['tenant_members'] LOOP
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
