-- ppts 邮箱自助注册账户与凭证（0029，2026-09-16；B5-M4 邮箱自助注册）
-- 设计（决策 ①A/②A/③A）：
--   - users：全局邮箱注册表（一个邮箱 = 一个账户），控制面注册表，不启用 RLS（与 tenants 同口径）。
--   - credentials：按租户隔离的密码凭证；RLS 沿用项目统一策略 app.tenant_id::uuid；
--     email 全局唯一（一个邮箱不能跨租户重复注册）；pepper 来自环境变量，不落库。
--   - 登录需按 email 跨租户定位凭证，但 credentials 受 RLS 保护无法直接全局读，
--     故提供 SECURITY DEFINER 函数 auth_lookup_credential(email) 在所有者权限下查询
--     （函数所有者为迁移角色，绕过 credentials RLS），仅回传该邮箱对应的租户/用户/哈希。

-- 用户全局注册表（无 RLS）
CREATE TABLE IF NOT EXISTS users (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    email      text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- 租户密码凭证（RLS 隔离）
CREATE TABLE IF NOT EXISTS credentials (
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id       text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email         text NOT NULL,
    password_hash text NOT NULL,
    algo          text NOT NULL DEFAULT 'bcrypt',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

ALTER TABLE credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE credentials FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON credentials;
CREATE POLICY tenant_isolation ON credentials
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

-- 一个邮箱全局唯一：既防跨租户重复注册，也作为登录定位约束。
CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_email_global ON credentials (email);

GRANT SELECT, INSERT, UPDATE, DELETE ON users TO ppts_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON credentials TO ppts_app;

-- 登录按 email 跨租户定位凭证（绕过 credentials RLS，仅回传必要字段）。
-- SECURITY DEFINER 以函数所有者（迁移角色）权限执行，ppts_app 调用时仍受 RLS 约束，
-- 仅能通过本函数按 email 取回自身凭证哈希，不能直接全表扫描 credentials。
CREATE OR REPLACE FUNCTION auth_lookup_credential(p_email text)
RETURNS TABLE(tenant_id uuid, user_id text, password_hash text)
LANGUAGE sql
SECURITY DEFINER
AS $$
    SELECT c.tenant_id, c.user_id, c.password_hash
    FROM credentials c
    WHERE c.email = p_email;
$$;

REVOKE ALL ON FUNCTION auth_lookup_credential(text) FROM public;
GRANT EXECUTE ON FUNCTION auth_lookup_credential(text) TO ppts_app;
