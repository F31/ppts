-- ppts 迁移账号账号化（0012，2026-09-13；G3-1 / ADR-019）
-- 目标：固化 ppts_migrator（迁移/owner）与 ppts_app（运行）的职责分离。
-- 本迁移可由 postgres 等发布 owner 执行；后续迁移建议使用 ppts_migrator。

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        CREATE ROLE ppts_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
    END IF;
END $$;

GRANT USAGE, CREATE ON SCHEMA public TO ppts_migrator;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'tenants',
        'projects', 'source_revisions', 'jobs', 'job_steps',
        'narration_scripts', 'narration_segments', 'artifacts', 'uploads',
        'tenant_quotas', 'quota_reservations', 'usage_ledger', 'audit_events', 'tenant_members'
    ] LOOP
        EXECUTE format('ALTER TABLE IF EXISTS %I OWNER TO ppts_migrator', t);
    END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ppts_app;

-- 测试库的 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON ALL TABLES IN SCHEMA public TO ppts_app;
    END IF;
END $$;
