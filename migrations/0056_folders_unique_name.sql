-- 分组名在租户内必须唯一。
--
-- 应用层一直先查后插，但并发请求仍会写出两条同名分组，随后列表出现重复项、
-- RenameFolder 的唯一冲突映射也永远走不到。先合并存量重名（项目搬到最早那条），
-- 再加唯一索引；这样应用层预检查继续负责友好报错，数据库负责兜底。

WITH ranked AS (
    SELECT id, tenant_id, name,
           row_number() OVER (PARTITION BY tenant_id, name ORDER BY created_at, id) AS rn,
           first_value(id) OVER (PARTITION BY tenant_id, name ORDER BY created_at, id) AS keeper
    FROM folders
)
UPDATE projects p
SET folder_id = ranked.keeper
FROM ranked
WHERE p.folder_id = ranked.id AND ranked.rn > 1;

WITH ranked AS (
    SELECT id, tenant_id, name,
           row_number() OVER (PARTITION BY tenant_id, name ORDER BY created_at, id) AS rn
    FROM folders
)
DELETE FROM folders f
USING ranked
WHERE f.id = ranked.id AND ranked.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_folders_tenant_name ON folders (tenant_id, name);
