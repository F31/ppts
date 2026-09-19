ALTER TABLE folders ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;

UPDATE folders
SET sort_order = (
    SELECT COUNT(*)
    FROM folders f2
    WHERE f2.tenant_id = folders.tenant_id
      AND (f2.name < folders.name OR (f2.name = folders.name AND f2.id <= folders.id))
) - 1;

CREATE INDEX IF NOT EXISTS idx_folders_tenant_sort ON folders (tenant_id, sort_order, name);
