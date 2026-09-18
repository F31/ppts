-- ppts 任务事件表（0019，2026-09-13；G3-9 WatchEvents 单调序号）
-- 目标：WatchEvents 使用独立事件序号（bigserial），避免复用 updated_at 同毫秒并发漏发。
-- 事件在任务状态/进度变更事务内写入（Create/Complete/ScheduleRetry/Cancel/RetryFailed/UpdateProgress），
-- snapshot 保存事件时刻的任务全量快照，客户端可断点续传（after_seq）。

CREATE TABLE IF NOT EXISTS job_events (
    seq        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants(id),
    project_id uuid NOT NULL,
    job_id     uuid NOT NULL,
    state      text NOT NULL,
    progress   int  NOT NULL DEFAULT 0,
    snapshot   jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_job_events_watch
    ON job_events(tenant_id, project_id, seq);

ALTER TABLE job_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_events FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON job_events;
CREATE POLICY tenant_isolation ON job_events
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ppts_migrator') THEN
        ALTER TABLE job_events OWNER TO ppts_migrator;
    END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON job_events TO ppts_app;

DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON job_events TO ppts_app;
        -- 注意：seq 是 IDENTITY 列，其序列所有权跟随表（内部依赖），
        -- ALTER SEQUENCE ... OWNER 会被 PG 拒绝（SQLSTATE 0A000），故不设。
    END IF;
END $$;
