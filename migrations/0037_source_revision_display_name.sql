-- PPTS：源版本显示名称（允许用户自定义 PPT 名称，持久化到 DB）。
ALTER TABLE source_revisions ADD COLUMN display_name text NOT NULL DEFAULT '';
