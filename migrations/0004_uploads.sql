-- ppts 上传会话（0004，2026-09-12；G1-1）
-- 授权直传需要服务端先分配受限对象键与签名链接，再在 CompleteUpload 时校验
-- 大小/哈希/租户所有权后才创建源版本与解析任务。会话状态持久化保证重试幂等。

CREATE TABLE IF NOT EXISTS uploads (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL,
    project_id         uuid NOT NULL,
    filename           text NOT NULL,
    content_type       text NOT NULL DEFAULT '',
    size_bytes         bigint NOT NULL,
    delete_source_after boolean NOT NULL DEFAULT false, -- V4.0 §13.1 处理后删除源文件
    state              text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','completed','aborted')),
    object_key         text NOT NULL,
    source_revision_id text NOT NULL DEFAULT '', -- CompleteUpload 成功后回填
    job_id             text NOT NULL DEFAULT '',   -- 解析任务 ID（幂等键 = upload id）
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_uploads_tenant ON uploads(tenant_id, project_id, created_at DESC);

ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ppts_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON uploads TO ppts_app;
