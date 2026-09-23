-- ppts 账号模型扩展（0043，2026-09-23）：邮箱/手机分列 + 邮箱验证 + 密码重置 + 会话吊销。
--
-- 背景：0029 用 users.email / credentials.email 单列同时存邮箱与手机号（同列共存），
-- 无法区分账号形态、无法做"邮箱验证"、也无处挂密码重置令牌。本迁移：
--   1) users/credentials 的 email 改为可空，新增 phone 列（各自全局唯一，部分索引）；
--   2) users 新增 email_verified_at / phone_verified_at / token_version / status；
--   3) 新增 auth_tokens（邮箱验证 / 密码重置，令牌只存 SHA-256 哈希，单次使用）；
--   4) auth_lookup_credential 改为按 email 或 phone 命中（登录自动识别账号形态）。
--
-- 兼容：既有行 email 均非空且已满足约束；新增列均有默认值/NULL，无需回填。

-- ── users（全局注册表，无 RLS）──────────────────────────────────────────────
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone text;
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone_verified_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';
ALTER TABLE users ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

-- 至少一个账号标识（邮箱或手机）。
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_account_present;
ALTER TABLE users ADD CONSTRAINT users_account_present
    CHECK (email IS NOT NULL OR phone IS NOT NULL);
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_valid;
ALTER TABLE users ADD CONSTRAINT users_status_valid
    CHECK (status IN ('active', 'disabled'));

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone_unique
    ON users (phone) WHERE phone IS NOT NULL;

-- ── credentials（按租户隔离，RLS）──────────────────────────────────────────
ALTER TABLE credentials ALTER COLUMN email DROP NOT NULL;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS phone text;
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_account_present;
ALTER TABLE credentials ADD CONSTRAINT credentials_account_present
    CHECK (email IS NOT NULL OR phone IS NOT NULL);

CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_phone_global
    ON credentials (phone) WHERE phone IS NOT NULL;

-- ── auth_tokens：邮箱验证 / 密码重置（跨租户，无 RLS；仅存令牌哈希）─────────
CREATE TABLE IF NOT EXISTS auth_tokens (
    id         text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose    text NOT NULL CHECK (purpose IN ('email_verify', 'password_reset')),
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_tokens_hash ON auth_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_user ON auth_tokens (user_id, purpose, created_at DESC);
-- 同一用户同一用途只保留最新一条有效令牌：签发新令牌时旧令牌应由应用删除（此处仅索引）。

GRANT SELECT, INSERT, UPDATE, DELETE ON auth_tokens TO ppts_app;

-- ── 登录定位：按 email 或 phone 命中（自动识别账号形态）────────────────────
DROP FUNCTION IF EXISTS auth_lookup_credential(text);
CREATE FUNCTION auth_lookup_credential(p_account text)
RETURNS TABLE(tenant_id uuid, user_id text, password_hash text)
LANGUAGE sql
SECURITY DEFINER
AS $$
    SELECT c.tenant_id, c.user_id, c.password_hash
    FROM credentials c
    WHERE c.email = p_account OR c.phone = p_account
    LIMIT 1;
$$;
REVOKE ALL ON FUNCTION auth_lookup_credential(text) FROM public;
GRANT EXECUTE ON FUNCTION auth_lookup_credential(text) TO ppts_app;

-- 密码重置：credentials 受 RLS 保护，按 user_id 直改会被拒；提供 SECURITY DEFINER 函数
-- 在所有者权限下更新该用户的密码哈希（单用户通常一条凭证），返回受影响行数。
CREATE OR REPLACE FUNCTION auth_set_password(p_user_id text, p_password_hash text)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    affected integer;
BEGIN
    UPDATE credentials SET password_hash = p_password_hash, updated_at = now()
     WHERE user_id = p_user_id;
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END;
$$;
REVOKE ALL ON FUNCTION auth_set_password(text, text) FROM public;
GRANT EXECUTE ON FUNCTION auth_set_password(text, text) TO ppts_app;

-- 测试库的 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON auth_tokens TO ppts_app;
    END IF;
END $$;
