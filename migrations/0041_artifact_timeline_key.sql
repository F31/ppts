-- 0040_artifact_timeline_key.sql
-- 目标：成品库内嵌预览（MP4 播放 / Web 讲解工程播放）。
-- 制品需要能定位到"该次导出所用的时间轴"，才能用与下载内容同源的数据构建播放清单；
-- 此前 artifacts 只有 object_key：前端只能下载，按 projectId 取"最新讲解"则可能与旧成品不同源
-- （导出后项目又生成过新讲解时，预览会放到另一版时间轴）。
--
-- 值为 timeline 对象键（{tenant}/{project}/timeline/...json），导出时由 ExportHandler 从任务快照写入；
-- 空串表示未知（历史行），此时前端隐藏"预览"按钮，降级为仅下载。
--
-- 仅在既有表上加列：表级授权（见 0003_artifacts.sql 的 GRANT ON artifacts）已覆盖新列，
-- 无需重新 GRANT，也无需改 RLS 策略。

ALTER TABLE artifacts ADD COLUMN IF NOT EXISTS timeline_key text NOT NULL DEFAULT '';
