-- 动画规格（M1，2026-10-28；与 pg 0051 对齐）
-- 单租户 SQLite profile：无 RLS，隔离靠 tenant_id 列 + 调用方上下文（与 narration_scripts 一致）。
CREATE TABLE IF NOT EXISTS animation_revisions (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL,
    project_id         TEXT NOT NULL,
    slide_id           TEXT NOT NULL,
    language           TEXT NOT NULL DEFAULT 'zh-CN',
    source_revision_no INTEGER NOT NULL DEFAULT 0,
    revision           INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft', 'approved', 'locked')),
    preset_id          TEXT NOT NULL DEFAULT '',
    intensity          REAL NOT NULL DEFAULT 1.0
                       CHECK (intensity >= 0.5 AND intensity <= 1.5),
    tracks             TEXT NOT NULL DEFAULT '[]',
    rendered_revision  INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, project_id, source_revision_no, slide_id, language)
);

CREATE INDEX IF NOT EXISTS idx_animation_revisions_slide
    ON animation_revisions (tenant_id, project_id, slide_id);
