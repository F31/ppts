-- PPTS SQLite：发音词典平台种子（0015，对齐 PostgreSQL 0046）
--
-- SQLite 无法 ALTER COLUMN DROP NOT NULL，故重建表：
--  1) 新增 is_platform_default 列（NOT NULL DEFAULT false）；
--  2) tenant_id 从 NOT NULL 放宽为可空（平台默认行 NULL，不占用租户命名空间）；
--  3) 重建索引；插入平台种子行。
-- 本表无外键引用（narration_segments.dictionary_id 仅为普通列，无 FK 约束），可安全重建。
CREATE TABLE pronunciation_dictionaries_new (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT,
    name       TEXT NOT NULL,
    rules      TEXT NOT NULL DEFAULT '[]',
    is_platform_default INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

INSERT INTO pronunciation_dictionaries_new
    (id, tenant_id, name, rules, is_platform_default, created_at, updated_at)
SELECT id, tenant_id, name, rules, 0, created_at, updated_at
  FROM pronunciation_dictionaries;

DROP TABLE pronunciation_dictionaries;
ALTER TABLE pronunciation_dictionaries_new RENAME TO pronunciation_dictionaries;

CREATE INDEX idx_pronunciation_dictionaries_tenant
    ON pronunciation_dictionaries (tenant_id);
CREATE INDEX idx_pronunciation_platform_default
    ON pronunciation_dictionaries (is_platform_default);

INSERT INTO pronunciation_dictionaries (id, tenant_id, name, rules, is_platform_default)
SELECT lower(hex(randomblob(16))), NULL, '平台默认发音词典', '[]', 1
WHERE NOT EXISTS (SELECT 1 FROM pronunciation_dictionaries WHERE is_platform_default = 1);