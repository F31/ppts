-- 任务表读侧索引补齐（与 pg 0019 对齐，2026-11-08）
--
-- 0001 只为领取路径建了索引（idx_jobs_claim / idx_jobs_project），读侧两条高频查询
-- 一直没有索引可用：
--
--   1. 事件订阅（EventsSince）：WHERE tenant_id=? AND project_id=? AND seq>?
--      ORDER BY seq —— 每有一个前端在看任务进度就打一次，无索引时全表扫描
--      job_events 再排序。事件只增不删，扫描成本随时间单调上升。
--   2. 列表页筛选（ListPage / PhaseCounts）：WHERE tenant_id=? [AND project_id=?]
--      [AND phase=?] —— 无索引时把该租户全部任务拉出来过滤/分组。phase 是主要的
--      筛选维度（解析/讲稿/合成/导出各自一屏）。
--
-- 列序按等值列在前、范围/排序列在后：phase 索引带 created_at 是为了让「按阶段筛选 +
-- 按创建时间排序」的 keyset 分页不额外排序；job_events 的 seq 是自增主键，
-- (tenant_id, project_id, seq) 同时满足过滤与 ORDER BY seq。
--
-- 不建 (tenant_id, project_id, updated_at)：UpdatedSince 已被 idx_jobs_project 的
-- 前缀收窄到单项目范围，排序量级是「一个项目的任务数」，等 EXPLAIN 量出真实成本再谈。
CREATE INDEX IF NOT EXISTS idx_job_events_watch ON job_events(tenant_id, project_id, seq);
CREATE INDEX IF NOT EXISTS idx_jobs_phase ON jobs(tenant_id, project_id, phase, created_at);
