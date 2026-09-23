-- PPTS SQLite：项目归档元信息（0014，对齐 PostgreSQL 0045）
ALTER TABLE projects ADD COLUMN archived_at TEXT;
ALTER TABLE projects ADD COLUMN archived_by TEXT;
