-- G3 模型网关：TTS/LLM 供应商配置（endpoint/key/模型/音色）Web 可视化配置与持久化。
-- 平台默认行 tenant_id = 零 UUID（与 Web 开发身份一致），租户行覆盖平台行。
-- 隔离策略与 pronunciation_dictionaries 一致：应用层按 tenant_id 显式过滤；
-- key 仅以 AES-GCM 密文保存（加密/解密与 AAD 绑定在应用层，见 internal/gateway）。
CREATE TABLE IF NOT EXISTS model_gateways (
    tenant_id       uuid NOT NULL,
    name            text NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('tts','llm')),
    provider        text NOT NULL DEFAULT 'openai_compatible',
    base_url        text NOT NULL,
    encrypted_creds bytea NOT NULL,
    model           text NOT NULL,
    vision_model    text NOT NULL DEFAULT '',
    voice           text NOT NULL DEFAULT '',
    sample_rate     int  NOT NULL DEFAULT 0,
    is_default      boolean NOT NULL DEFAULT false,
    enabled         boolean NOT NULL DEFAULT true,
    version         int   NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, name, kind)
);
CREATE INDEX IF NOT EXISTS idx_model_gateways_tenant_kind
    ON model_gateways (tenant_id, kind);
-- 每租户每 kind 至多一个默认网关。
CREATE UNIQUE INDEX IF NOT EXISTS uq_model_gateways_default
    ON model_gateways (tenant_id, kind) WHERE is_default;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE model_gateways OWNER TO ppts_migrator;
    END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON model_gateways TO ppts_app;

DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON model_gateways TO ppts_app;
    END IF;
END $$;
