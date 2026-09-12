-- ppts 导出成品（0003，2026-09-12；G1-7）
-- Artifact 不可变；同一固定输入快照与格式重复提交返回同一成品。

CREATE TABLE IF NOT EXISTS artifacts (
    id            uuid PRIMARY KEY,
    tenant_id     uuid NOT NULL,
    project_id    uuid NOT NULL,
    snapshot_hash text NOT NULL,
    format        text NOT NULL,
    object_key    text NOT NULL,
    content_hash  text NOT NULL,
    size_bytes    bigint NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, project_id, snapshot_hash, format)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_tenant_project ON artifacts(tenant_id, project_id, created_at DESC);

ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ppts_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON artifacts TO ppts_app;
