-- 0027_artifact_duration.sql
-- 目标（B3-M1 遗留项）：成品需要展示「实际时长」。设计方案 V1_6 §332 要求成品与版本列表展示
-- 「版本/记录标识、源文件版本、语言、音色、实际时长和时间」，而 artifacts 此前只有 size_bytes，
-- 成品页的时长一栏因此一直无法展示（只能留空或省略）。
--
-- 口径：duration_ms = 该成品所绑定时间轴的实际时长（media.Timeline.DurationUS / 1000），
-- 由 app.ExportHandler 在写成品时落库。MP4 的画面长度、SRT/VTT 的字幕跨度、Web 工程的播放总长
-- 都来自同一条时间轴，故四种格式含义一致，无需按格式分支。
--
-- 0 表示「未知 / 未记录」（既有历史行保持默认值 0）——界面显示「—」，不伪造时长。
--
-- 仅在既有表上加列：表级授权（见 0003_artifacts.sql 的 GRANT ON artifacts）已覆盖新列，
-- 无需重新 GRANT，也无需改 RLS 策略。

ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS duration_ms bigint NOT NULL DEFAULT 0;
