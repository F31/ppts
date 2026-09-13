-- ppts BYOS 凭据加密存储（0015，2026-09-13；G3-6）
-- 目标：客户自带对象存储凭据只以密文保存；运行时读取需应用层解密密钥。

CREATE TABLE IF NOT EXISTS byos_credentials (
    tenant_id        uuid NOT NULL REFERENCES tenants(id),
    credential_id    text NOT NULL,
    backend          text NOT NULL,
    encrypted_config bytea NOT NULL,
    kms_key_id       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, credential_id)
);
CREATE INDEX IF NOT EXISTS idx_byos_credentials_tenant_backend ON byos_credentials(tenant_id, backend);

ALTER TABLE byos_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE byos_credentials FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON byos_credentials;
CREATE POLICY tenant_isolation ON byos_credentials
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE byos_credentials OWNER TO ppts_migrator;
    END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON byos_credentials TO ppts_app;

DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON byos_credentials TO ppts_app;
    END IF;
END $$;
