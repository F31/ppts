-- 0010_artifact_source_meta.sql（SQLite 单租户 profile，对应 PG 0042）
-- 成品行冗余源版本号与展示名，供成品库按"PPT 名称 + 版本"展示。
-- 历史行 revision_no=0 / source_display_name='' 表示未知，前端回退到项目名称。
-- 注意：SQLite 的 ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，迁移按序执行且不重复运行。
ALTER TABLE artifacts ADD COLUMN revision_no INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artifacts ADD COLUMN source_display_name TEXT NOT NULL DEFAULT '';
