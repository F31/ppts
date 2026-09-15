-- B2 M3 ⑥：无备注页讲稿来源选择持久化（tenant 隔离，遵循 0006 RLS 纵深防御）。
-- 当某页无演讲者备注时，用户显式指定驱动草稿生成的文本来源（版式/标题/正文/备注/自定义）。
-- 选择经原生 HTTP 端点持久化，并在 GenerateDraft 时注入 ScriptDraftSnapshot.Sources，
-- 由 script_draft worker 在 pgText/pgAnchors 中尊重该来源。

CREATE TABLE IF NOT EXISTS slide_script_sources (
    tenant_id   uuid NOT NULL,
    project_id  uuid NOT NULL,
    slide_id    text NOT NULL,
    source      text NOT NULL CHECK (source IN ('layout', 'title', 'body', 'notes', 'custom')),
    custom_text text NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_id, slide_id)
);

ALTER TABLE slide_script_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE slide_script_sources FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON slide_script_sources;
CREATE POLICY tenant_isolation ON slide_script_sources
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON slide_script_sources TO ppts_app;
