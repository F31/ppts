-- ppts 公开作品发布表（0008，2026-09-15；V1.6 公开区 C-1）
-- 支撑"官方精选"(featured) 与"用户作品"(user) 双 Tab 公开展示：
--   - 公开只读（匿名）仅返回 status='approved' 行，跨租户聚合；
--   - 写操作（发布/审核/精选/删除）在租户 RLS 上下文执行（源项目所属租户）。
-- 遵循 0006 的 RLS 纵深防御：运行账号 ppts_app 非 owner、NOBYPASSRLS，
-- 所有访问走 current_setting('app.tenant_id') 判定；缺失即拒绝。

CREATE TABLE IF NOT EXISTS publications (
    id               uuid PRIMARY KEY,
    tenant_id        uuid NOT NULL REFERENCES tenants(id),
    project_id       uuid NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('featured', 'user')),
    status           text NOT NULL CHECK (status IN ('draft', 'pending', 'approved', 'rejected')),
    title            text NOT NULL,
    summary          text NOT NULL DEFAULT '',
    cover_object_key text,
    sort_order       int NOT NULL DEFAULT 0,
    created_by       text NOT NULL,
    reviewed_by      text,
    reviewed_at      timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
-- 公开列表按"已批准 + 类型 + 排序 + 时间"索引，仅覆盖 approved 行（索引更小、扫描更快）。
CREATE INDEX IF NOT EXISTS idx_publications_approved ON publications (status, kind, sort_order, created_at)
    WHERE status = 'approved';
CREATE INDEX IF NOT EXISTS idx_publications_project ON publications (tenant_id, project_id);

ALTER TABLE publications ENABLE ROW LEVEL SECURITY;
ALTER TABLE publications FORCE ROW LEVEL SECURITY;

-- 匿名只读：任意角色可读 status='approved' 的行（跨租户聚合由本策略允许）。
-- 与 tenant_write 同为 PERMISSIVE SELECT 策略，OR 合并：approved 即放行。
DROP POLICY IF EXISTS publications_public_read ON publications;
CREATE POLICY publications_public_read ON publications FOR SELECT
    USING (status = 'approved');

-- 租户内写：发布/审核/精选/删除需在 app.tenant_id 上下文，且行租户须匹配。
DROP POLICY IF EXISTS publications_tenant_write ON publications;
CREATE POLICY publications_tenant_write ON publications FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON publications TO ppts_app;
