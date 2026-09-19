-- PPTS SQLite：源版本记录原始上传文件名（对齐 PostgreSQL 0036）。
ALTER TABLE source_revisions ADD COLUMN filename TEXT NOT NULL DEFAULT '';
