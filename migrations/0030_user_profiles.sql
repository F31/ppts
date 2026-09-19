-- ppts 成员档案扩展（0030，2026-09-19；成员与权限列表富字段）
-- 目标：成员列表展示 用户名/姓名/性别/出生年月/邮箱/电话/创建时间。
--   - users 为全局注册表（无 RLS），成员档案挂在 users.id 上；
--   - 列表查询在 tenant_members 的 RLS 租户隔离内 LEFT JOIN users + user_profiles，
--     只返回当前租户成员对应的档案，不会跨租户泄露。
--   - user_profiles 本身无 tenant_id，不启用 RLS（无租户列）。

CREATE TABLE IF NOT EXISTS user_profiles (
    user_id    text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    username   text,
    full_name  text,
    gender     text CHECK (gender IS NULL OR gender IN ('male', 'female', 'other', 'unknown')),
    birth_date date,
    phone      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_profiles_username ON user_profiles (username);

GRANT SELECT, INSERT, UPDATE, DELETE ON user_profiles TO ppts_app;

-- 测试库的 PG 集成测试需要清表重放；生产运行账号不授予 TRUNCATE。
DO $$
BEGIN
    IF current_database() = 'ppts_test' THEN
        GRANT TRUNCATE ON user_profiles TO ppts_app;
    END IF;
END $$;
