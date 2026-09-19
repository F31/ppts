-- ppts 标签 + 分组体系（0031，2026-09-19；#94 标签+分组，P1）
-- 目标：项目可挂多对多标签（跨分组检索）+ 归属单一文件夹（组织），
--       标签/分组均为租户私有，沿用项目既有 RLS 纵深防御（0006）。
--   - tags：租户内 name 唯一；color 可选（前端色板）。
--   - folders：租户内分组（单归属）；删除时项目 folder_id 回落 NULL（未分类），不级联删项目。
--   - project_tags：项目↔标签多对多，带 tenant_id 便于 RLS；删标签/项目级联清理。
--   - projects.folder_id：可空外键，删分组时 SET NULL。

CREATE TABLE IF NOT EXISTS tags (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    name       text NOT NULL,
    color      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL,
    name       text NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS project_tags (
    tenant_id  uuid NOT NULL,
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tag_id     uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, tag_id)
);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS folder_id uuid REFERENCES folders(id) ON DELETE SET NULL;

-- ---- RLS（tenant 隔离，FORCE 纵深防御） ----
ALTER TABLE tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE tags FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON tags;
CREATE POLICY tenant_isolation ON tags
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE folders ENABLE ROW LEVEL SECURITY;
ALTER TABLE folders FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON folders;
CREATE POLICY tenant_isolation ON folders
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE project_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_tags FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON project_tags;
CREATE POLICY tenant_isolation ON project_tags
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- 索引（分组/标签列表与过滤）
CREATE INDEX IF NOT EXISTS idx_tags_tenant ON tags (tenant_id);
CREATE INDEX IF NOT EXISTS idx_folders_tenant ON folders (tenant_id);
CREATE INDEX IF NOT EXISTS idx_project_tags_tag ON project_tags (tag_id);
CREATE INDEX IF NOT EXISTS idx_project_tags_tenant ON project_tags (tenant_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON tags TO ppts_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON folders TO ppts_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON project_tags TO ppts_app;

-- 测试库 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON tags TO ppts_app;
        GRANT TRUNCATE ON folders TO ppts_app;
        GRANT TRUNCATE ON project_tags TO ppts_app;
    END IF;
END $$;
