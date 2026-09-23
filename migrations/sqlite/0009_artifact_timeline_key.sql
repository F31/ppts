-- 0040_artifact_timeline_key.sql（SQLite 单租户 profile，对应 PG 0040）
-- 成品记录携带导出所用时间轴键，供成品库内嵌预览（MP4 / Web 讲解工程）；
-- 空串 = 历史行未知，前端隐藏预览按钮、降级为仅下载。
ALTER TABLE artifacts ADD COLUMN timeline_key TEXT NOT NULL DEFAULT '';
