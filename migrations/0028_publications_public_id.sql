-- B5-M3 公开发布与撤回（A29）
-- 目标：
--   1) public_id：32 字符 base62 随机串（A-Za-z0-9，不可反推），与内部主键 id(UUID) 解耦；
--      匿名访问一律走 public_id，杜绝用内部主键枚举/反推（满足 A29「publicId 不可反推」）。
--   2) 状态机新增 withdrawn（撤回即失效）：匿名只读策略 publications_public_read 仅放行
--      status='approved'，置 withdrawn 后匿名读自然拒绝，无需额外清理（CDN 清理按 B5-C2 豁免）。
--   3) 删除级联失效：管理员 DELETE 直接删行，匿名 URL 立即失效（既有行为，本迁移不改）。
--
-- 执行顺序：先扩状态机，再加列与回填，最后建唯一约束。

-- 扩展状态机：新增 withdrawn
ALTER TABLE publications
  DROP CONSTRAINT IF EXISTS publications_status_check,
  ADD CONSTRAINT publications_status_check
    CHECK (status IN ('draft', 'pending', 'approved', 'rejected', 'withdrawn'));

-- public_id + withdrawn_at 列
ALTER TABLE publications ADD COLUMN IF NOT EXISTS public_id text;
ALTER TABLE publications ADD COLUMN IF NOT EXISTS withdrawn_at timestamptz;

-- 生成 32 字符 base62 随机串（不可反推），与后端 store.go 的 genPublicID 同源语义
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION ppts_gen_public_id() RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
  chars text := 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  buf   bytea := gen_random_bytes(32);
  out   text  := '';
  i     int;
  n     int;
BEGIN
  FOR i IN 0..31 LOOP
    n := get_byte(buf, i);
    out := out || substr(chars, (n % 62) + 1, 1);
  END LOOP;
  RETURN out;
END;
$$;

-- 回填存量行（含未批准），保证唯一约束可建
UPDATE publications SET public_id = ppts_gen_public_id() WHERE public_id IS NULL;

ALTER TABLE publications ALTER COLUMN public_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_publications_public_id ON publications(public_id);

DROP FUNCTION IF EXISTS ppts_gen_public_id();
