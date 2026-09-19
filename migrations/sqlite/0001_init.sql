-- PPTS SQLite schema（单租户精简 profile，PPTS_DB_DRIVER=sqlite）
--
-- 与 PostgreSQL 迁移的设计差异（有意为之）：
--   1. 无 RLS / 无角色 / 无 SECURITY DEFINER 函数：单租户 + 单进程，隔离由应用层显式 tenant_id 过滤承担；
--   2. 类型映射：uuid→TEXT, timestamptz→TEXT(RFC3339 毫秒 UTC), jsonb→TEXT(JSON), boolean→INTEGER(0/1),
--      numeric→REAL, bytea→BLOB, text[]→TEXT(JSON 数组), bigint/int→INTEGER；
--   3. 无 gen_random_uuid()/now() 默认：ID 与时间戳由 Go 侧生成（见 internal/db/sqlite），保证与 PG 语义一致；
--   4. 时间戳默认统一为 RFC3339 毫秒 UTC，便于 Go 直接 time.Parse(time.RFC3339Nano)。
--
-- 幂等：全部 CREATE ... IF NOT EXISTS。

-- ─── 基础：本地租户与本地用户（单租户 profile 的固定身份）──────────────────────
-- 由应用启动时 ensureLocalIdentity() 播种，ID 为固定常量（见 internal/db/local.go）。
CREATE TABLE IF NOT EXISTS tenants (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    policy       TEXT NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','deleted')),
    suspended_at TEXT,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS users (
    id         TEXT PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ─── 项目域 ──────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS projects (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL REFERENCES tenants(id),
    owner_user            TEXT NOT NULL,
    title                 TEXT NOT NULL,
    current_revision      INTEGER NOT NULL DEFAULT 0,
    policy                TEXT NOT NULL DEFAULT '{}',
    archived              INTEGER NOT NULL DEFAULT 0,
    delete_source_after   INTEGER NOT NULL DEFAULT 0,
    source_retention_days INTEGER,
    folder_id             TEXT REFERENCES folders(id) ON DELETE SET NULL,
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_projects_tenant ON projects(tenant_id, created_at);

CREATE TABLE IF NOT EXISTS source_revisions (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects(id),
    tenant_id         TEXT NOT NULL,
    revision_no       INTEGER NOT NULL,
    source_hash       TEXT NOT NULL,
    object_key        TEXT NOT NULL,
    parser_version    TEXT NOT NULL,
    page_count        INTEGER NOT NULL DEFAULT 0,
    upload_id         TEXT NOT NULL DEFAULT '',
    source_deleted_at TEXT,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (project_id, revision_no)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_source_revisions_upload
    ON source_revisions(tenant_id, upload_id) WHERE upload_id <> '';
CREATE INDEX IF NOT EXISTS idx_source_revisions_retention
    ON source_revisions(tenant_id, source_deleted_at, created_at);

CREATE TABLE IF NOT EXISTS slide_script_sources (
    tenant_id   TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    slide_id    TEXT NOT NULL,
    source      TEXT NOT NULL CHECK (source IN ('layout','title','body','notes','custom')),
    custom_text TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, project_id, slide_id)
);

-- ─── 标签 / 分组（单租户下仍可用）────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tags (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    color      TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS folders (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS project_tags (
    tenant_id  TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tag_id     TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, tag_id)
);

-- ─── 协作（单租户下保留表结构，语义退化为本地 owner）─────────────────────────
CREATE TABLE IF NOT EXISTS project_collaborators (
    tenant_id        TEXT NOT NULL,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id          TEXT NOT NULL,
    role             TEXT NOT NULL CHECK (role IN ('viewer','reviewer','editor','admin')),
    invited_by       TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_accessed_at TEXT,
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE IF NOT EXISTS project_share_links (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL,
    project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    token              TEXT NOT NULL UNIQUE,
    access_mode        TEXT NOT NULL DEFAULT 'view_only' CHECK (access_mode IN ('view_only','view_and_comment')),
    password_hash      TEXT,
    password_protected INTEGER NOT NULL DEFAULT 0,
    expires_at         TEXT,
    revoked            INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_accessed_at   TEXT
);

-- ─── 讲稿域 ──────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS narration_scripts (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    project_id     TEXT NOT NULL,
    slide_id       TEXT NOT NULL,
    language       TEXT NOT NULL DEFAULT 'zh-CN',
    mode           TEXT NOT NULL DEFAULT 'original',
    status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','locked')),
    revision       INTEGER NOT NULL DEFAULT 0,
    audio_revision INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, project_id, slide_id, language)
);

CREATE TABLE IF NOT EXISTS narration_segments (
    id           TEXT PRIMARY KEY,
    script_id    TEXT NOT NULL REFERENCES narration_scripts(id) ON DELETE CASCADE,
    tenant_id    TEXT NOT NULL,
    segment_id   TEXT NOT NULL,
    display_text TEXT NOT NULL DEFAULT '',
    spoken_text  TEXT NOT NULL DEFAULT '',
    source_refs  TEXT NOT NULL DEFAULT '[]',
    source_anchors TEXT NOT NULL DEFAULT '[]',
    status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','locked')),
    revision     INTEGER NOT NULL DEFAULT 0,
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (script_id, segment_id)
);

-- ─── 成品 / 对象清单 ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS artifacts (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    project_id    TEXT NOT NULL,
    snapshot_hash TEXT NOT NULL,
    format        TEXT NOT NULL,
    object_key    TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    size_bytes    INTEGER NOT NULL,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, project_id, snapshot_hash, format)
);

CREATE TABLE IF NOT EXISTS object_inventory (
    object_key   TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id),
    project_id   TEXT NOT NULL,
    revision     TEXT NOT NULL,
    asset_type   TEXT NOT NULL,
    asset_id     TEXT NOT NULL,
    ext          TEXT NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    content_type TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ─── 上传 ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS uploads (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    project_id          TEXT NOT NULL,
    filename            TEXT NOT NULL,
    content_type        TEXT NOT NULL DEFAULT '',
    size_bytes          INTEGER NOT NULL,
    delete_source_after INTEGER NOT NULL DEFAULT 0,
    state               TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','completed','aborted')),
    object_key          TEXT NOT NULL,
    source_revision_id  TEXT NOT NULL DEFAULT '',
    job_id              TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ─── 任务队列（单写者：无 FOR UPDATE SKIP LOCKED / SECURITY DEFINER）──────────
CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    kind            TEXT NOT NULL,
    state           TEXT NOT NULL CHECK (state IN (
                        'queued','running','retry_wait','waiting_review',
                        'succeeded','failed','cancel_requested','canceled',
                        'unknown_provider_result')),
    input_snapshot  TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    attempt         INTEGER NOT NULL DEFAULT 0,
    lease_owner     TEXT,
    lease_until     TEXT,
    fencing_token   INTEGER NOT NULL DEFAULT 0,
    run_at          TEXT,
    progress        INTEGER NOT NULL DEFAULT 0,
    phase           TEXT NOT NULL DEFAULT '',
    affected_pages  TEXT NOT NULL DEFAULT '[]',
    traceparent     TEXT NOT NULL DEFAULT '',
    last_error      TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, idempotency_key, kind)
);
CREATE INDEX IF NOT EXISTS idx_jobs_claim ON jobs(state, run_at, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_project ON jobs(tenant_id, project_id, created_at);

CREATE TABLE IF NOT EXISTS job_steps (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    tenant_id  TEXT NOT NULL,
    step_type  TEXT NOT NULL,
    step_key   TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('pending','success','skipped','failed')),
    result_ref TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (job_id, step_key)
);

CREATE TABLE IF NOT EXISTS job_events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id),
    project_id TEXT NOT NULL,
    job_id     TEXT NOT NULL,
    state      TEXT NOT NULL,
    progress   INTEGER NOT NULL DEFAULT 0,
    snapshot   TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ─── 用量 / 配额（单租户：本地默认不限量）────────────────────────────────────
CREATE TABLE IF NOT EXISTS usage_ledger (
    id                   TEXT PRIMARY KEY,
    tenant_id            TEXT NOT NULL,
    logical_operation_id TEXT NOT NULL,
    usage_kind           TEXT NOT NULL,
    quantity             REAL NOT NULL DEFAULT 0,
    unit                 TEXT NOT NULL DEFAULT '',
    price_version        TEXT NOT NULL DEFAULT '',
    user_amount          REAL NOT NULL DEFAULT 0,
    supplier_cost        REAL NOT NULL DEFAULT 0,
    currency             TEXT NOT NULL DEFAULT '',
    created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, logical_operation_id, usage_kind)
);

CREATE TABLE IF NOT EXISTS tenant_quotas (
    tenant_id      TEXT NOT NULL REFERENCES tenants(id),
    usage_kind     TEXT NOT NULL DEFAULT 'gen_seconds',
    limit_units    REAL NOT NULL DEFAULT -1,
    reserved_units REAL NOT NULL DEFAULT 0,
    consumed_units REAL NOT NULL DEFAULT 0,
    price_version  TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, usage_kind)
);

CREATE TABLE IF NOT EXISTS quota_reservations (
    id                   TEXT PRIMARY KEY,
    tenant_id            TEXT NOT NULL,
    logical_operation_id TEXT NOT NULL,
    usage_kind           TEXT NOT NULL,
    reserved_units       REAL NOT NULL,
    state                TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','settled','released')),
    created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (tenant_id, logical_operation_id, usage_kind)
);

-- ─── 审计（单租户：本地记录，无归档）────────────────────────────────────────
CREATE TABLE IF NOT EXISTS audit_events (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id),
    actor_user    TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    metadata      TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_audit_tenant ON audit_events(tenant_id, created_at DESC);

-- ─── 模型网关 ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS model_gateways (
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('tts','llm')),
    provider        TEXT NOT NULL DEFAULT 'openai_compatible',
    base_url        TEXT NOT NULL,
    encrypted_creds BLOB NOT NULL,
    model           TEXT NOT NULL,
    vision_model    TEXT NOT NULL DEFAULT '',
    voice           TEXT NOT NULL DEFAULT '',
    sample_rate     INTEGER NOT NULL DEFAULT 0,
    is_default      INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, name, kind)
);

-- ─── 发音词典 ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS pronunciation_dictionaries (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    rules      TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ─── 公开发布（单租户：本地可选，保留表）─────────────────────────────────────
CREATE TABLE IF NOT EXISTS publications (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id),
    project_id       TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('featured','user')),
    status           TEXT NOT NULL CHECK (status IN ('draft','pending','approved','rejected')),
    title            TEXT NOT NULL,
    summary          TEXT NOT NULL DEFAULT '',
    cover_object_key TEXT,
    sort_order       INTEGER NOT NULL DEFAULT 0,
    public_id        TEXT,
    withdrawn_at     TEXT,
    created_by       TEXT NOT NULL,
    reviewed_by      TEXT,
    reviewed_at      TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ─── 自带存储（BYOS，单租户可选）─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS byos_credentials (
    tenant_id        TEXT NOT NULL REFERENCES tenants(id),
    credential_id    TEXT NOT NULL,
    backend          TEXT NOT NULL,
    encrypted_config BLOB NOT NULL,
    kms_key_id       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, credential_id)
);

-- ─── 认证/成员（单租户 profile 下由本地身份 stub，保留表兼容）─────────────────
CREATE TABLE IF NOT EXISTS credentials (
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    algo          TEXT NOT NULL DEFAULT 'bcrypt',
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE IF NOT EXISTS tenant_members (
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('owner','admin','editor','reviewer','viewer')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE IF NOT EXISTS user_profiles (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    username   TEXT,
    full_name  TEXT,
    gender     TEXT CHECK (gender IS NULL OR gender IN ('male','female','other','unknown')),
    birth_date TEXT,
    phone      TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
