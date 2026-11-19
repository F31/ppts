-- 0023_artifact_display_offset.sql（SQLite 单租户 profile，对应 PG 0057）
-- 成品行冗余"字幕显示偏移"(毫秒，正=高亮延后)，0=无偏移。
-- 与 PG 0057 同语义：导出时固化，三处播放器共用；改偏移=重新导出新成品。
-- SQLite 的 ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS，迁移按序执行且不重复运行。
ALTER TABLE artifacts ADD COLUMN display_offset_ms INTEGER NOT NULL DEFAULT 0;
