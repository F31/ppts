-- PPTS：源版本记录原始上传文件名（对齐 uploads.filename，供前端展示 PPT 名称）。
ALTER TABLE source_revisions ADD COLUMN IF NOT EXISTS filename text NOT NULL DEFAULT '';
