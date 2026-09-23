-- 消息服务配置（0044，2026-09-23）：发件箱（SMTP）与短信网关，按租户保存。
--   - config：非敏感字段（host/port/username/from/tls 等），jsonb；
--   - encrypted_creds：敏感字段（SMTP 密码 / 短信密钥）以 AES-GCM 密文保存，
--     AAD 绑定 (tenant_id, channel)，见 internal/messaging；
--   - 平台默认行 tenant_id = 零 UUID，租户行覆盖平台行（用于新用户注册/验证邮件兜底）。
-- 隔离策略与 model_gateways 一致：应用层按 tenant_id 显式过滤（不启用 RLS）。
CREATE TABLE IF NOT EXISTS message_channels (
    tenant_id       uuid NOT NULL,
    channel         text NOT NULL CHECK (channel IN ('email','sms')),
    enabled         boolean NOT NULL DEFAULT false,
    config          jsonb NOT NULL DEFAULT '{}'::jsonb,
    encrypted_creds bytea NOT NULL DEFAULT ''::bytea,
    version         integer NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, channel)
);
CREATE INDEX IF NOT EXISTS idx_message_channels_tenant ON message_channels (tenant_id);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE message_channels OWNER TO ppts_migrator;
    END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON message_channels TO ppts_app;

DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON message_channels TO ppts_app;
    END IF;
END $$;
