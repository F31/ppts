-- 讲稿按「源版本」隔离（M17）。
--
-- 背景：slide_id 取自 PPTX 文件内部 id（p:sldId@id），只在单个文件内唯一。
-- 同一份稿子改版重传、或不同文件恰好同 id（如 v4/v5 都有 slide-258）时，
-- 不同版本的页面会共用同一 slide_id —— 讲稿/来源选择因此互相覆盖。
-- 故 narration_scripts / slide_script_sources 增加 source_revision_no，
-- 唯一键纳入该列。存量行沿用 DEFAULT 0 表示 legacy：读时作为回退可见
-- （某版本首次写入后，该版本即拥有独立讲稿，legacy 不再影响它）。
--
-- 注意：narration_segments 以 ON DELETE CASCADE 引用 narration_scripts。
-- 在 foreign_keys=ON 下直接 DROP 父表会触发级联、删空所有分段。
-- 因此先把分段暂存到备份表并清空子表，重建父表后再回填。
CREATE TABLE narration_segments_backup_0008 AS SELECT * FROM narration_segments;
DELETE FROM narration_segments;

CREATE TABLE narration_scripts_new (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    project_id     TEXT NOT NULL,
    slide_id       TEXT NOT NULL,
    language       TEXT NOT NULL DEFAULT 'zh-CN',
    mode           TEXT NOT NULL DEFAULT 'original',
    status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','locked')),
    revision       INTEGER NOT NULL DEFAULT 0,
    audio_revision INTEGER NOT NULL DEFAULT 0,
    source_revision_no INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, project_id, source_revision_no, slide_id, language)
);
INSERT INTO narration_scripts_new
    (id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, source_revision_no, updated_at, created_at)
SELECT id, tenant_id, project_id, slide_id, language, mode, status, revision, audio_revision, 0, updated_at, created_at
  FROM narration_scripts;
DROP TABLE narration_scripts;
ALTER TABLE narration_scripts_new RENAME TO narration_scripts;

INSERT INTO narration_segments
    (id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision, updated_at)
SELECT id, script_id, tenant_id, segment_id, display_text, spoken_text, source_refs, source_anchors, status, revision, updated_at
  FROM narration_segments_backup_0008;
DROP TABLE narration_segments_backup_0008;

-- slide_script_sources：无子表引用，直接重建并纳入 source_revision_no。
CREATE TABLE slide_script_sources_new (
    tenant_id   TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    slide_id    TEXT NOT NULL,
    source_revision_no INTEGER NOT NULL DEFAULT 0,
    source      TEXT NOT NULL CHECK (source IN ('layout','title','body','notes','custom')),
    custom_text TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, project_id, source_revision_no, slide_id)
);
INSERT INTO slide_script_sources_new
    (tenant_id, project_id, slide_id, source_revision_no, source, custom_text, updated_at)
SELECT tenant_id, project_id, slide_id, 0, source, custom_text, updated_at
  FROM slide_script_sources;
DROP TABLE slide_script_sources;
ALTER TABLE slide_script_sources_new RENAME TO slide_script_sources;
