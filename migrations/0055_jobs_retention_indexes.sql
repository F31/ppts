-- jobs/job_events 保留清理的索引（P3-6，2026-11-09）
--
-- 调度函数 ppts_claim_next_job 全表扫，终态行只增不减会线性拖慢每次轮询
-- （基线：16 万行时空转心跳约 30ms）。worker 的保留清理按「终态 + updated_at 早于
-- 保留期」分批删 jobs，并显式同批删 job_events（job_events.job_id 无外键，删 jobs
-- 不级联事件）。两个删除都依赖下面这两类索引，否则在大租户下各自退化成全表 seq scan，
-- 清理本身反被增长拖垮：
--
--   1) idx_jobs_retention：jobs(tenant_id, updated_at) WHERE state IN 终态。
--      DeleteExpiredJobs 的 doomed CTE 就是
--        WHERE tenant_id=$1 AND state IN ('succeeded','failed','canceled') AND updated_at < $2
--      部分索引把扫描面从「全租户 jobs」收敛到「本租户终态且过期的少数行」。
--      终态集合与 pipeline.JobState.Terminal() 逐字一致；unknown_provider_result 不在内。
--
--   2) idx_job_events_job：job_events(tenant_id, job_id)。
--      events 删除是 job_id IN (SELECT id FROM doomed)（≤500 行/批），没有 job_id 索引时
--      每批都会全表扫 job_events（16 万任务对应 ~64 万事件）。tenant_id 前缀配合 RLS 上下文。
--
-- 幂等：第二次执行 CREATE INDEX IF NOT EXISTS 匹配已存在索引即跳过，不报错。
-- 非 CONCURRENTLY：与既有 jobs 索引（0026 idx_jobs_phase）一致走迁移事务；大表上会短暂
-- 取 ACCESS EXCLUSIVE 锁，属一次性迁移开销，可接受。

CREATE INDEX IF NOT EXISTS idx_jobs_retention
    ON jobs(tenant_id, updated_at)
    WHERE state IN ('succeeded', 'failed', 'canceled');

CREATE INDEX IF NOT EXISTS idx_job_events_job
    ON job_events(tenant_id, job_id);
