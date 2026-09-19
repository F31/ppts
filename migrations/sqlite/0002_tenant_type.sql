-- PPTS SQLite：租户类型（0002，对齐 PostgreSQL 0034；个人/组织账号）
-- 个人账号 = 只有一个成员的 tenant；type 仅驱动前端入口显隐，后端权限模型不按 type 分叉。
ALTER TABLE tenants ADD COLUMN type TEXT NOT NULL DEFAULT 'personal'
    CHECK (type IN ('personal','organization'));

UPDATE tenants SET type = 'organization'
 WHERE id IN (SELECT tenant_id FROM tenant_members GROUP BY tenant_id HAVING COUNT(*) > 1);
