-- 动画规格（M1，2026-10-28；《动画视频扩展-设计方案 V1.0》§5.6，与 sqlite 0018 对齐）
--
-- 一页一份规格，隔离维度与讲稿完全同构：(tenant, project, source_revision_no, slide, language)。
-- slide_id 只在单个 PPTX 内唯一，必须靠 source_revision_no 区分版本（同 0040 的理由）；
-- source_revision_no = 0 表示 legacy，读路径按回退可见。
-- tracks 存 JSONB：单页轨道 3–15 条，查询永远是"按页取整份"，没有按 clip 聚合的需求，
-- 拆表只会增加事务与迁移成本（§5.2 已论证）。

CREATE TABLE IF NOT EXISTS animation_revisions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL,
    project_id         uuid NOT NULL,
    slide_id           text NOT NULL,
    language           text NOT NULL DEFAULT 'zh-CN',
    source_revision_no integer NOT NULL DEFAULT 0,
    revision           bigint NOT NULL DEFAULT 0,          -- 乐观并发版本，每次写入 +1
    status             text NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft', 'approved', 'locked')),
    preset_id          text NOT NULL DEFAULT '',           -- 空 = 自定义（不套预设）
    intensity          double precision NOT NULL DEFAULT 1.0
                       CHECK (intensity >= 0.5 AND intensity <= 1.5),
    tracks             jsonb NOT NULL DEFAULT '[]'::jsonb,
    rendered_revision  bigint NOT NULL DEFAULT 0,          -- 最近一次成功渲染对应的 revision
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT animation_revisions_scope_uniq
        UNIQUE (tenant_id, project_id, source_revision_no, slide_id, language)
);

CREATE INDEX IF NOT EXISTS idx_animation_revisions_slide
    ON animation_revisions (tenant_id, project_id, slide_id);

-- RLS：与 0006 同一套策略，靠事务局部 app.tenant_id 判定；缺失即拒绝。
ALTER TABLE animation_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE animation_revisions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON animation_revisions;
CREATE POLICY tenant_isolation ON animation_revisions
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- 新表不在既有 GRANT 的覆盖范围内（ALL TABLES 只作用于当时的存量表），必须显式授权。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON animation_revisions TO ppts_app;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE animation_revisions OWNER TO ppts_migrator;
    END IF;
END $$;
