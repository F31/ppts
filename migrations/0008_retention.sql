-- ppts 数据保留与到期清理（0008，2026-09-12；G3-7）
-- 目标（V4.0 §13.1/§12.5）：执行"处理完成后删除源文件"与源文件保留期，
-- 清理长期滞留的临时对象，只保留必要派生产物。
-- 以 source_deleted_at 标记源对象已删除（保留不可变版本行用于追溯，避免重复删除）。

ALTER TABLE source_revisions
    ADD COLUMN IF NOT EXISTS source_deleted_at timestamptz;

CREATE INDEX IF NOT EXISTS idx_source_revisions_retention
    ON source_revisions(tenant_id, source_deleted_at, created_at);
