-- ppts RLS 纵深防御（0006，2026-09-12；G3-1 / ADR-019）
-- 目标（V4.0 §12.1）：
--   1) 运行账号 ppts_app 非 owner、NOSUPERUSER NOBYPASSRLS；
--   2) 所有租户业务表 ENABLE + FORCE ROW LEVEL SECURITY；
--   3) 策略以事务局部 current_setting('app.tenant_id') 判定，缺失即拒绝；
--   4) 业务事务由 internal/tenant.Run 执行 set_config(..., true)。
-- 说明：tenants 为控制面注册表（当前无 TenantService），本迁移不对其启用 RLS。

-- 运行角色 bootstrap（部署/CI 可先行创建；此处仅缺失时补齐，不修改既有属性）。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_app') THEN
        CREATE ROLE ppts_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
END $$;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'projects', 'source_revisions', 'jobs', 'job_steps',
        'narration_scripts', 'narration_segments', 'artifacts', 'uploads', 'usage_ledger'
    ] LOOP
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
