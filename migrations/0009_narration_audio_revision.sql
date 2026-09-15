-- B2 M1：记录讲稿最近一次配音对应的脚本修订号，用于 stale 判定
-- （当前输入 vs 音频关联修订比较：audio_revision < revision 表示配音可能过期）。
ALTER TABLE narration_scripts ADD COLUMN IF NOT EXISTS audio_revision bigint NOT NULL DEFAULT 0;

-- 核心创作前端按 (tenant, project, language) 列举讲稿以计算 stale 信号。
CREATE INDEX IF NOT EXISTS idx_narration_scripts_project_lang
  ON narration_scripts (tenant_id, project_id, language);
