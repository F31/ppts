-- ppts 私密分享与协作者（0032，2026-09-19；#95 私密分享/协作者，P1）
-- 目标：在「发布到公开作品广场」之外，提供**私密分享**能力（与公开发布完全不同的语义，
--       不共用一个开关，也不出现在公开广场）。
--   - project_collaborators：租户内项目协作者名单 + 项目级角色（viewer/reviewer/editor/admin）。
--   - project_share_links：项目私密分享链接（不可反推 token、访问模式、可选密码、有效期、可撤回）。
--
-- 安全设计（对应《实施计划》R-2/R-15 的越权风险）：
--   - 两张表均启用 FORCE RLS；协作者表纯租户隔离（只有租户上下文可见）。
--   - 分享链接表在租户隔离之外，额外加一条 **匿名只读策略** anon_read：
--       仅放行 revoked=false 且未过期的行，供匿名访问链路按 token 命中。
--     PG 的 permissive 策略是 OR 关系，故未设置 app.tenant_id 的匿名连接被租户策略拒绝、
--     但可被 anon_read 放行——与 publications_public_read 同一范式。
--   - token 为 32 位十六进制（去横线 uuid），不可反推，杜绝用内部主键枚举。
--   - password_hash **永不出现在匿名查询的 SELECT 列里**；匿名链路只取 password_protected
--     布尔位判断是否需要口令，真正的 bcrypt 比对在拿到 tenant_id 后回到租户上下文执行。
--     口令比对恒定失败文案，不区分"链接不存在/口令错误"，防枚举。

CREATE TABLE IF NOT EXISTS project_collaborators (
    tenant_id        uuid NOT NULL,
    project_id       uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role             text NOT NULL CHECK (role IN ('viewer', 'reviewer', 'editor', 'admin')),
    invited_by       uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_accessed_at timestamptz,
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE IF NOT EXISTS project_share_links (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL,
    project_id         uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    token              text NOT NULL UNIQUE DEFAULT replace(gen_random_uuid()::text, '-', ''),
    access_mode        text NOT NULL DEFAULT 'view_only'
                       CHECK (access_mode IN ('view_only', 'view_and_comment')),
    password_hash      text,
    password_protected boolean NOT NULL DEFAULT false,
    expires_at         timestamptz,
    revoked            boolean NOT NULL DEFAULT false,
    created_by         uuid,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_accessed_at   timestamptz
);

-- ---- RLS（tenant 隔离，FORCE 纵深防御） ----
ALTER TABLE project_collaborators ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_collaborators FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON project_collaborators;
CREATE POLICY tenant_isolation ON project_collaborators
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

ALTER TABLE project_share_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_share_links FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON project_share_links;
CREATE POLICY tenant_isolation ON project_share_links
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- 匿名只读：无租户上下文时，按 token 命中"未撤回且未过期"的链接行。
DROP POLICY IF EXISTS anon_read ON project_share_links;
CREATE POLICY anon_read ON project_share_links
    FOR SELECT
    USING (revoked = false AND (expires_at IS NULL OR expires_at > now()));

-- 索引（按项目列协作者/链接）
CREATE INDEX IF NOT EXISTS idx_project_collaborators_tenant ON project_collaborators (tenant_id);
CREATE INDEX IF NOT EXISTS idx_project_collaborators_user ON project_collaborators (user_id);
CREATE INDEX IF NOT EXISTS idx_share_links_project ON project_share_links (project_id);
CREATE INDEX IF NOT EXISTS idx_share_links_tenant ON project_share_links (tenant_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON project_collaborators TO ppts_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON project_share_links TO ppts_app;

-- 测试库 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON project_collaborators TO ppts_app;
        GRANT TRUNCATE ON project_share_links TO ppts_app;
    END IF;
END $$;
