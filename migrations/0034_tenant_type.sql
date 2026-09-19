-- ppts 租户类型（0034，2026-09-19；个人/组织账号）
-- 目标：以 tenants.type 区分「个人账号」（单成员，前端隐藏成员管理/邀请协作者入口）
--       与「组织账号」（可邀请成员并分配角色）。
-- 设计：个人账号 = 只有一个成员的 tenant，与组织结构完全一致，不新增表/字段；
--       后端权限模型不按 type 分叉（隔离仍统一按 tenant_id），type 仅驱动前端入口显隐。
--
-- 回填：多成员租户判为 organization，其余保持默认 personal，
--       避免既有组织的成员管理入口被误隐藏。

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS type text NOT NULL DEFAULT 'personal'
        CHECK (type IN ('personal','organization'));

UPDATE tenants SET type = 'organization'
 WHERE id IN (SELECT tenant_id FROM tenant_members GROUP BY tenant_id HAVING count(*) > 1);
