-- ppts 调度并发上限（0016，2026-09-13；G3-2 / ADR-018）
-- 目标：ppts_claim_next_job 增加按租户 per-tenant 并发上限，避免单租户占满队列。
-- 仅对 narration 任务执行 max_concurrent_jobs 上限（与业务语义一致）；其他任务类型不限制。

DROP FUNCTION IF EXISTS ppts_claim_next_job(text, integer);
CREATE FUNCTION ppts_claim_next_job(p_lease_owner text, p_lease_seconds integer)
RETURNS TABLE (
    id uuid,
    tenant_id uuid,
    project_id uuid,
    kind text,
    state text,
    input_snapshot text,
    idempotency_key text,
    attempt int,
    lease_owner text,
    lease_until timestamptz,
    fencing_token bigint,
    run_at timestamptz,
    progress int,
    last_error jsonb,
    created_at timestamptz,
    updated_at timestamptz
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
    WITH tenant_load AS (
        SELECT tenant_id,
               count(*) AS running_count,
               count(*) FILTER (WHERE kind='narration') AS running_narration
        FROM jobs
        WHERE state IN ('running','cancel_requested')
        GROUP BY tenant_id
    ), tenant_cap AS (
        SELECT id,
               COALESCE(NULLIF((policy->>'max_concurrent_jobs')::int, 0), 2147483647) AS cap
        FROM tenants
    ), candidate AS (
        SELECT j.id
        FROM jobs j
        LEFT JOIN tenant_load l ON l.tenant_id = j.tenant_id
        LEFT JOIN tenant_cap c ON c.id = j.tenant_id
        WHERE (j.run_at IS NULL OR j.run_at <= now())
          AND (
            (j.state IN ('queued','retry_wait') AND (j.lease_until IS NULL OR j.lease_until < now()))
            OR (j.state IN ('running','cancel_requested') AND j.lease_until < now())
          )
          AND (
            j.kind <> 'narration'
            OR (COALESCE(l.running_narration, 0) < COALESCE(c.cap, 2147483647))
          )
        ORDER BY coalesce(l.running_count, 0), j.created_at
        LIMIT 1
        FOR UPDATE OF j SKIP LOCKED
    )
    UPDATE jobs j SET
        state = CASE WHEN j.state='cancel_requested' THEN 'cancel_requested' ELSE 'running' END,
        attempt = j.attempt + 1,
        lease_owner = p_lease_owner,
        lease_until = now() + make_interval(secs => greatest(p_lease_seconds, 1)),
        fencing_token = j.fencing_token + 1,
        updated_at = now()
    FROM candidate c
    WHERE j.id = c.id
    RETURNING j.id, j.tenant_id, j.project_id, j.kind, j.state, j.input_snapshot,
              j.idempotency_key, j.attempt, j.lease_owner, j.lease_until,
              j.fencing_token, j.run_at, j.progress, j.last_error, j.created_at, j.updated_at;
$$;

ALTER FUNCTION ppts_claim_next_job(text, integer) OWNER TO ppts_migrator;

REVOKE ALL ON FUNCTION ppts_claim_next_job(text, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ppts_claim_next_job(text, integer) TO ppts_scheduler;

-- 测试库使用 ppts_app 连接跑 PG 集成测试，仅测试库允许调用该调度函数。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT EXECUTE ON FUNCTION ppts_claim_next_job(text, integer) TO ppts_app;
    END IF;
END $$;
