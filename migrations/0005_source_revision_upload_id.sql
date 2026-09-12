-- ppts 源版本上传幂等（0005，2026-09-12；G1 收尾修复）
-- 背景：CompleteUpload 的调用序列为 CreateSourceRevision → jobs.Create → uploads.Complete。
-- 若在中间崩溃/失败后客户端重试，会话仍为 pending，会再次 CreateSourceRevision，
-- 导致同一内容生成重复源版本并多递增 current_revision。
-- 修复：以 upload_id 作为上传链路的幂等键（IngestService 等非上传路径留空，不受影响）。

ALTER TABLE source_revisions
    ADD COLUMN IF NOT EXISTS upload_id text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_source_revisions_upload
    ON source_revisions(tenant_id, upload_id)
    WHERE upload_id <> '';

GRANT SELECT, INSERT, UPDATE, DELETE ON source_revisions TO ppts_app;
