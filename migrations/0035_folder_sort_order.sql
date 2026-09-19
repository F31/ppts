ALTER TABLE folders ADD COLUMN IF NOT EXISTS sort_order int NOT NULL DEFAULT 0;

WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY tenant_id ORDER BY name, created_at, id) - 1 AS rn
    FROM folders
)
UPDATE folders f
SET sort_order = ranked.rn
FROM ranked
WHERE f.id = ranked.id;

CREATE INDEX IF NOT EXISTS idx_folders_tenant_sort ON folders (tenant_id, sort_order, name);
