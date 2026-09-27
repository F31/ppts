-- PPTS SQLite：上下文替换规则（0016，对齐 PostgreSQL 0048）
--
-- 单租户 SQLite profile 下 tenant_id 恒为单租户值；本表结构与 PG 对齐
-- （tenant_id 可空，NULL=平台默认），保持两套方言 schema 一致。
CREATE TABLE IF NOT EXISTS contextual_rules (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT,
    pattern    TEXT NOT NULL,
    replacement TEXT NOT NULL,
    priority   INTEGER NOT NULL DEFAULT 10,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_contextual_rules_tenant
    ON contextual_rules (tenant_id);
