-- M5 上下文替换规则（V3.0 §3.3 数据驱动；对标 itntext TSV 资源表）
--
-- 与发音词典的区别：contextual_rules 是「正则 → 产出模板」的上下文相关替换
-- （如 `([0-9]+)(%)` → `百分之$1`），作用于普通 span 的局部匹配，而发音词典是
-- 整 span 字面量 ReplaceAll。二者由 textnorm 引擎按各自语义并行执行。
--
-- 数据建模：
--   - pattern 为 Go regexp（支持捕获组）；replacement 为产出模板（$1 / ${name}）；
--   - priority 决定规则顺序（小值先执行，与 RegisterPattern 语义一致）；
--   - enabled=false 时跳过（语义同发音词典 Rule）；
--   - tenant_id 可空：NULL 行为平台默认（对标 pronunciation_dictionaries
--     is_platform_default，但不引入额外布尔列——tenant_id IS NULL 即平台行）。
--   - 隔离与发音词典一致：应用层按 tenant_id 显式过滤，无 RLS；平台行天然不可被
--     租户查询命中（WHERE tenant_id = $1 不匹配 NULL）。
CREATE TABLE IF NOT EXISTS contextual_rules (
    id         uuid PRIMARY KEY,
    tenant_id  uuid,
    pattern    text NOT NULL,
    replacement text NOT NULL,
    priority   integer NOT NULL DEFAULT 10,
    enabled    boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_contextual_rules_tenant
    ON contextual_rules (tenant_id);
CREATE INDEX IF NOT EXISTS idx_contextual_rules_platform
    ON contextual_rules (tenant_id) WHERE tenant_id IS NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON contextual_rules TO ppts_app;

-- 测试库的 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON contextual_rules TO ppts_app;
    END IF;
END $$;
