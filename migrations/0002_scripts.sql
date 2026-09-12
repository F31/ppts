-- ppts 讲稿域（0002，2026-09-12；G1-4）
-- 讲稿按 (tenant, project, slide, language) 一份，带 revision 计数与锁定；
-- 分段是可复用最小单位（V4.0 §7.1 Segment）。

CREATE TABLE IF NOT EXISTS narration_scripts (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL,
    project_id  uuid NOT NULL,
    slide_id    text NOT NULL,
    language    text NOT NULL DEFAULT 'zh-CN',
    mode        text NOT NULL DEFAULT 'original', -- original/polish/ai_generated
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','locked')),
    revision    bigint NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, project_id, slide_id, language)
);

CREATE TABLE IF NOT EXISTS narration_segments (
    id           uuid PRIMARY KEY,
    script_id    uuid NOT NULL REFERENCES narration_scripts(id) ON DELETE CASCADE,
    tenant_id    uuid NOT NULL,
    segment_id   text NOT NULL,          -- seg-07-02（稳定 ID）
    display_text text NOT NULL DEFAULT '',
    spoken_text  text NOT NULL DEFAULT '',
    source_refs  text[] NOT NULL DEFAULT '{}', -- slide-07/shape-12
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','locked')),
    revision     int NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (script_id, segment_id)
);
CREATE INDEX IF NOT EXISTS idx_segments_script ON narration_segments(script_id, segment_id);