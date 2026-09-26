-- V2.8 §6 路径一：发音词典平台种子（textnorm 平台默认词典）
--
-- 数据建模决定（评审拍板，弃哨兵字符串）：
--   - 新增 is_platform_default bool 列区分「平台默认行 / 租户自定义行」；
--   - 平台默认行 tenant_id 置 NULL（不占用任何真实租户命名空间），
--     租户自定义行保持真实 tenant_id。
-- 隔离说明：本表无 RLS 策略（应用层按 tenant_id 显式过滤），
--   tenant_id IS NULL 的行天然不会被租户查询命中（WHERE tenant_id = $1），
--   故平台行只对显式读取它的 LoadPlatformDefault 可见。
ALTER TABLE pronunciation_dictionaries
  ALTER COLUMN tenant_id DROP NOT NULL;

ALTER TABLE pronunciation_dictionaries
  ADD COLUMN IF NOT EXISTS is_platform_default boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_pronunciation_platform_default
  ON pronunciation_dictionaries (is_platform_default);

-- 平台种子：平台级默认发音词典（GPU/CPU 型号、单位、专有名词读音）。
-- 行由迁移写入；租户自定义规则在其自定义词典中，LoadTenantDefault 不受影响。
INSERT INTO pronunciation_dictionaries (id, tenant_id, name, rules, is_platform_default)
SELECT gen_random_uuid(), NULL, '平台默认发音词典',
       jsonb_build_array()::jsonb, true
WHERE NOT EXISTS (
  SELECT 1 FROM pronunciation_dictionaries WHERE is_platform_default = true
);