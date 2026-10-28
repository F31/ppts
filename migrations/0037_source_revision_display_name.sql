-- PPTS：源版本显示名称（允许用户自定义 PPT 名称，持久化到 DB）。
-- IF NOT EXISTS：CI 的 postgres job 会把全部迁移在同一库上再放一遍做幂等校验，
-- 不加这一句第二遍就撞 "column already exists"。
ALTER TABLE source_revisions ADD COLUMN IF NOT EXISTS display_name text NOT NULL DEFAULT '';
