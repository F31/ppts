-- PPTS SQLite：消息服务配置（0013，对齐 PostgreSQL 0044；发件箱/短信网关）
CREATE TABLE IF NOT EXISTS message_channels (
    tenant_id       TEXT NOT NULL,
    channel         TEXT NOT NULL CHECK (channel IN ('email','sms')),
    enabled         INTEGER NOT NULL DEFAULT 0,
    config          TEXT NOT NULL DEFAULT '{}',
    encrypted_creds BLOB,
    version         INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, channel)
);
CREATE INDEX IF NOT EXISTS idx_message_channels_tenant ON message_channels (tenant_id);
