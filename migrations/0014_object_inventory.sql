-- ppts 对象清单（0014，2026-09-13；G3-6 存储成本/占用）
-- 目标：持久化对象存储写入元数据，补齐临时 work/audio/export/archive 等对象的大小可见性。

CREATE TABLE IF NOT EXISTS object_inventory (
    object_key   text PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenants(id),
    project_id   text NOT NULL,
    revision     text NOT NULL,
    asset_type   text NOT NULL,
    asset_id     text NOT NULL,
    ext          text NOT NULL DEFAULT '',
    size_bytes   bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    content_type text NOT NULL DEFAULT '',
    content_hash text NOT NULL DEFAULT '',
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_object_inventory_tenant_asset ON object_inventory(tenant_id, asset_type, updated_at DESC);

ALTER TABLE object_inventory ENABLE ROW LEVEL SECURITY;
ALTER TABLE object_inventory FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON object_inventory;
CREATE POLICY tenant_isolation ON object_inventory
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE object_inventory OWNER TO ppts_migrator;
    END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON object_inventory TO ppts_app;

DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON object_inventory TO ppts_app;
    END IF;
END $$;
