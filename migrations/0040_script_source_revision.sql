-- 讲稿按「源版本」隔离（M17，与 sqlite 0008 对齐）。
--
-- slide_id 取自 PPTX 文件内部 id（p:sldId@id），只在文件内唯一；改版重传或不同文件
-- 同 id 时不同版本会共用 slide_id，讲稿/来源选择互相覆盖。故唯一键纳入 source_revision_no，
-- 存量行沿用 DEFAULT 0 表示 legacy（读时回退可见）。
ALTER TABLE narration_scripts ADD COLUMN IF NOT EXISTS source_revision_no integer NOT NULL DEFAULT 0;
ALTER TABLE narration_scripts DROP CONSTRAINT IF EXISTS narration_scripts_tenant_id_project_id_slide_id_language_key;
ALTER TABLE narration_scripts ADD CONSTRAINT narration_scripts_scope_uniq
    UNIQUE (tenant_id, project_id, source_revision_no, slide_id, language);

ALTER TABLE slide_script_sources ADD COLUMN IF NOT EXISTS source_revision_no integer NOT NULL DEFAULT 0;
ALTER TABLE slide_script_sources DROP CONSTRAINT IF EXISTS slide_script_sources_pkey;
ALTER TABLE slide_script_sources ADD CONSTRAINT slide_script_sources_pkey
    PRIMARY KEY (tenant_id, project_id, source_revision_no, slide_id);
