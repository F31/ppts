-- ppts 队列积压只读函数（0017，2026-09-13；G3-8）
-- 目标：ppts_scheduler 连接可在不触碰业务表的情况下读取队列最老等待时长。

DROP FUNCTION IF EXISTS ppts_queue_backlog_seconds();
CREATE FUNCTION ppts_queue_backlog_seconds()
RETURNS double precision
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT EXTRACT(EPOCH FROM (now() - min(created_at)))
    FROM jobs
    WHERE state IN ('queued','retry_wait')
      AND (run_at IS NULL OR run_at <= now())
      AND (lease_until IS NULL OR lease_until < now());
$$;

ALTER FUNCTION ppts_queue_backlog_seconds() OWNER TO ppts_migrator;

REVOKE ALL ON FUNCTION ppts_queue_backlog_seconds() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ppts_queue_backlog_seconds() TO ppts_scheduler;

-- 测试库使用 ppts_app 连接跑 PG 集成测试，仅测试库允许调用该函数。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT EXECUTE ON FUNCTION ppts_queue_backlog_seconds() TO ppts_app;
    END IF;
END $$;
