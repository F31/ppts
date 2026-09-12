-- ppts 初始模式（0001，2026-09-12；G1 建模）
-- 原则（V4.0 §16）：多租户字段、授权上下文、持久化任务与账本唯一性从 G1 建模。
--   - 所有租户业务表含 tenant_id（uuid）
--   - 必要的唯一约束使用复合租户键
--   - RLS 在 0002 中添加（G3 加固清单项；G1 先建列与索引）

-- 租户
CREATE TABLE IF NOT EXISTS tenants (
    id          uuid PRIMARY KEY,
    name        text NOT NULL,
    policy      jsonb NOT NULL DEFAULT '{}'::jsonb, -- 存储后端/保留期/加密档位（V4.0 §12.4）
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- 项目
CREATE TABLE IF NOT EXISTS projects (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL REFERENCES tenants(id),
    owner_user         text NOT NULL,
    title              text NOT NULL,
    current_revision   bigint NOT NULL DEFAULT 0,
    policy             jsonb NOT NULL DEFAULT '{}'::jsonb,
    archived           boolean NOT NULL DEFAULT false,
    delete_source_after boolean NOT NULL DEFAULT false, -- V4.0 §13.1 处理后删除源文件
    source_retention_days int,                           -- 源文件保留期（独立于派生产物）
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_projects_tenant ON projects(tenant_id, created_at);

-- 源文件版本（不可变）
CREATE TABLE IF NOT EXISTS source_revisions (
    id             uuid PRIMARY KEY,
    project_id     uuid NOT NULL REFERENCES projects(id),
    tenant_id      uuid NOT NULL,
    revision_no    int NOT NULL,           -- src-03 → 3
    source_hash    text NOT NULL,
    object_key     text NOT NULL,
    parser_version text NOT NULL,
    page_count     int NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, revision_no)
);
CREATE INDEX IF NOT EXISTS idx_src_tenant ON source_revisions(tenant_id, project_id);

-- 任务（数据库为事实来源：V4.0 §10）
CREATE TABLE IF NOT EXISTS jobs (
    id               uuid PRIMARY KEY,
    tenant_id        uuid NOT NULL,
    project_id       uuid NOT NULL,
    kind             text NOT NULL, -- parse/render/script_draft/narration/export
    state            text NOT NULL CHECK (state IN (
                        'queued','running','retry_wait','waiting_review',
                        'succeeded','failed','cancel_requested','canceled',
                        'unknown_provider_result')),
    input_snapshot   text NOT NULL DEFAULT '',
    idempotency_key  text NOT NULL DEFAULT '',
    attempt          int NOT NULL DEFAULT 0,
    lease_owner      text,
    lease_until      timestamptz,
    fencing_token    bigint NOT NULL DEFAULT 0,
    run_at           timestamptz,          -- 定时运行（重试退避）
    progress         int NOT NULL DEFAULT 0,
    last_error       jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idempotency_key, kind) -- 幂等：同键同种目只入一次
);
CREATE INDEX IF NOT EXISTS idx_jobs_claim ON jobs(tenant_id, state, run_at, created_at)
    WHERE state = 'queued' OR state = 'retry_wait';
CREATE INDEX IF NOT EXISTS idx_jobs_project ON jobs(tenant_id, project_id, created_at DESC);

-- 任务步骤（重试只重跑未确认完成的步骤）
CREATE TABLE IF NOT EXISTS job_steps (
    id           uuid PRIMARY KEY,
    job_id       uuid NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    tenant_id    uuid NOT NULL,
    step_type    text NOT NULL, -- render/tts_segment/alignment/assembly/export
    step_key     text NOT NULL, -- snapshot+step_type+segment/config_hash
    state        text NOT NULL CHECK (state IN ('pending','success','skipped','failed')),
    result_ref   text NOT NULL DEFAULT '', -- 临时对象 ref；提交时与步骤事务一致
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (job_id, step_key)
);
CREATE INDEX IF NOT EXISTS idx_steps_job ON job_steps(job_id, step_type);

-- 用量账本（V4.0 §10.3：唯一键防重复结算）
CREATE TABLE IF NOT EXISTS usage_ledger (
    id                uuid PRIMARY KEY,
    tenant_id         uuid NOT NULL,
    logical_operation_id text NOT NULL,
    usage_kind        text NOT NULL, -- gen_seconds/tts_call/storage_bytes
    quantity          numeric NOT NULL DEFAULT 0,
    unit              text NOT NULL DEFAULT '',
    price_version     text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, logical_operation_id, usage_kind)
);