-- 成品级分享链接（M6-3，2026-10-29；《动画视频扩展-设计方案 V1.0》§20.7）
--
-- 与 0032 的 project_share_links 并存而不合并。理由（design ADR-1）：
--   project_share_links 分享的是「会变的项目」，本表分享的是「不变的那一版成片」。
--   若把两者塞进一张表用可空列区分，每个读路径都要先判类型再分支，而这类分支
--   不会被测满——「成品链接被当成项目链接处理」会安静地返回错误内容，而不是报错。
--
-- 安全设计沿用 0032，逐条对齐：
--   - FORCE RLS + 租户隔离策略；另加 anon_read 放行「未撤销且未过期」的行，
--     供匿名链路在无租户上下文时按 token 命中（PG permissive 策略是 OR 关系）。
--   - token 为 32 位十六进制（去横线 uuid），不可反推，杜绝按主键枚举。
--   - password_hash 永不出现在匿名查询的 SELECT 列里；匿名链路只看
--     password_protected 布尔位决定是否开口令门，bcrypt 比对回到租户上下文执行。
--   - 撤销/过期的判据只写在查询条件与 anon_read 策略里，应用层不重复判断。

CREATE TABLE IF NOT EXISTS artifact_share_links (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL,
    -- 锚点：成品，不是项目。成品删除时链接随行消失，不需要应用层清理。
    artifact_id        uuid NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    -- 冗余列：只为「按项目列出分享」与审计可读；不参与内容定位。
    project_id         uuid NOT NULL,
    -- 创建链接那一刻成品指向的对象键与内容哈希（**快照**）。
    --
    -- 只绑 artifact_id 不够：artifacts 的写路径是
    --   ON CONFLICT (tenant_id, project_id, snapshot_hash, format)
    --   DO UPDATE SET object_key = EXCLUDED.object_key ...
    -- 也就是「同一快照重新导出」会**原地改写**成品行。若分享端每次再去读
    -- artifacts.object_key，旧链接就会指向新产物——这正是本变更要消灭的行为。
    -- 门禁用例 TestArtifactShareAnchorsToArtifact 就是为了钉住它而写的，第一版实现
    -- 没存这两列，用例立刻红。
    object_key         text NOT NULL,
    content_hash       text NOT NULL,
    token              text NOT NULL UNIQUE DEFAULT replace(gen_random_uuid()::text, '-', ''),
    access_mode        text NOT NULL DEFAULT 'view_only'
                       CHECK (access_mode IN ('view_only', 'view_and_comment')),
    password_hash      text,
    password_protected boolean NOT NULL DEFAULT false,
    expires_at         timestamptz,
    revoked            boolean NOT NULL DEFAULT false,
    created_by         text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_accessed_at   timestamptz
);

-- ---- RLS（tenant 隔离，FORCE 纵深防御） ----
ALTER TABLE artifact_share_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifact_share_links FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON artifact_share_links;
CREATE POLICY tenant_isolation ON artifact_share_links
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- 匿名只读：与 0032 的 anon_read 同句式。这一条就是「撤销立即生效」的实现，
-- 应用层不再写第二份判断，否则两处迟早漂移。
DROP POLICY IF EXISTS anon_read ON artifact_share_links;
CREATE POLICY anon_read ON artifact_share_links
    FOR SELECT
    USING (revoked = false AND (expires_at IS NULL OR expires_at > now()));

CREATE INDEX IF NOT EXISTS idx_artifact_share_links_artifact ON artifact_share_links (artifact_id);
CREATE INDEX IF NOT EXISTS idx_artifact_share_links_project ON artifact_share_links (project_id);
CREATE INDEX IF NOT EXISTS idx_artifact_share_links_tenant ON artifact_share_links (tenant_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON artifact_share_links TO ppts_app;

-- 测试库 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON artifact_share_links TO ppts_app;
    END IF;
END
$$;
