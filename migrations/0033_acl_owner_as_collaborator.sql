-- ppts 项目级 ACL：回填 owner 为默认协作者（#96，P1）
-- 目标：让「协作者控制可见性」语义生效——所有现有项目的 owner 自动成为 admin 协作者。
-- 幂等：ON CONFLICT DO NOTHING，重复执行无副作用。
-- 安全：仅写入 project_collaborators，不动其他表；不改变现有协作者。

INSERT INTO project_collaborators (tenant_id, project_id, user_id, role, invited_by)
SELECT DISTINCT ON (p.id)
    p.tenant_id,
    p.id,
    p.owner_user,           -- text，与 project_collaborators.user_id(text) 一致
    'admin',                -- owner 默认最高项目级角色
    p.owner_user            -- invited_by 同 owner（不可追溯，仅便于审计）
FROM projects p
ORDER BY p.id, p.created_at  -- 每个项目取最早创建者（owner_user 本就唯一）
ON CONFLICT (project_id, user_id) DO NOTHING;

-- 给非 owner 的租户成员一个清晰的提示：项目可见性已变更，需被邀请才有访问权。
-- 不强制迁移现有非 owner 成员为协作者（保持最小改动），新创建项目自动有 owner 协作者。
