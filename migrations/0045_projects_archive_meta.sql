-- 项目归档元信息（0045，2026-09-24）：记录归档时间与操作人，供"已归档"列表展示。
ALTER TABLE projects ADD COLUMN IF NOT EXISTS archived_at timestamptz;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS archived_by text;
