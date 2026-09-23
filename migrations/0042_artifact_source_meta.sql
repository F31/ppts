-- 0042_artifact_source_meta.sql
-- 目标：成品库卡片按"PPT 名称 + 版本"精确展示（B5-M2 衍生需求）。
-- artifacts 此前无到 source_revisions 的外键，无法从成品反查版本号 / 展示名；
-- 故在成品行直接冗余这两列，由 narration 写入时间轴、export 落库（见 internal/app/{narration,export}.go）。
-- revision_no=0 / source_display_name='' 表示历史行未知，前端回退到项目名称。
-- 仅在既有表上加列：表级授权（见 0003_artifacts.sql 的 GRANT ON artifacts）与 RLS 策略已覆盖新列，
-- 无需重新 GRANT，也无需改 RLS 策略。

ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS revision_no integer NOT NULL DEFAULT 0;
ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS source_display_name text NOT NULL DEFAULT '';
