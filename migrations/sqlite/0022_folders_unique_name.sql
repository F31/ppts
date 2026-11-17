-- SQLite 侧与 PG 0056 同义：合并重名分组，再补唯一索引。
WITH ranked AS (
    SELECT id, tenant_id, name,
           row_number() OVER (PARTITION BY tenant_id, name ORDER BY created_at, id) AS rn,
           first_value(id) OVER (PARTITION BY tenant_id, name ORDER BY created_at, id) AS keeper
    FROM folders
)
UPDATE projects
SET folder_id = (SELECT keeper FROM ranked r WHERE r.id = projects.folder_id)
WHERE folder_id IN (SELECT id FROM ranked WHERE rn > 1);

WITH ranked AS (
    SELECT id, tenant_id, name,
           row_number() OVER (PARTITION BY tenant_id, name ORDER BY created_at, id) AS rn
    FROM folders
)
DELETE FROM folders
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

CREATE UNIQUE INDEX IF NOT EXISTS idx_folders_tenant_name ON folders (tenant_id, name);
