-- job_steps.state 新增取值 degraded（M9，与 PG 0039 对齐）。
--
-- SQLite 无法修改既有 CHECK 约束 → 按官方推荐流程重建表：新建 → 复制 → 删除 → 改名 → 重建索引。
-- 前置确认：job_steps 不被任何表/视图/触发器引用（只有它自己外键引用 jobs），故在
-- foreign_keys=ON（sqliteDSN 固定开启）下 DROP 旧表不会触发外键错误；索引 idx_steps_job
-- 会随 DROP TABLE 一并消失，必须在改名后重建。
CREATE TABLE job_steps_new (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    tenant_id  TEXT NOT NULL,
    step_type  TEXT NOT NULL,
    step_key   TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('pending','success','skipped','failed','degraded')),
    result_ref TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (job_id, step_key)
);

INSERT INTO job_steps_new (id, job_id, tenant_id, step_type, step_key, state, result_ref, updated_at)
SELECT id, job_id, tenant_id, step_type, step_key, state, result_ref, updated_at
  FROM job_steps;

DROP TABLE job_steps;

ALTER TABLE job_steps_new RENAME TO job_steps;

CREATE INDEX IF NOT EXISTS idx_steps_job ON job_steps(job_id, step_type);
