-- 任务中心全局视图：JobService.List 允许 project_id 为空（列出租户全部项目任务），
-- 排序为 created_at DESC；补租户级索引避免跨项目排序全表扫描。
CREATE INDEX IF NOT EXISTS idx_jobs_tenant_created ON jobs(tenant_id, created_at DESC);
