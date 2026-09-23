-- PPTS SQLite：账号模型扩展（0011，对齐 PostgreSQL 0043；邮箱/手机、验证、重置令牌）
--
-- 说明：SQLite 单租户模式不提供登录（server 不对 SQLite 挂载 /auth 路由），故此处仅做
-- 与 PG 尽量一致的**附加列/表**对齐；SQLite 无法低成本把 users.email 由 NOT NULL UNIQUE
-- 改为可空（需整表重建），故保留其 NOT NULL 约束。若未来 SQLite 也要开放注册需整表重建。

ALTER TABLE users ADD COLUMN phone TEXT;
ALTER TABLE users ADD COLUMN email_verified_at TEXT;
ALTER TABLE users ADD COLUMN phone_verified_at TEXT;
ALTER TABLE users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
-- SQLite 的 ALTER TABLE ADD COLUMN 不允许非恒定默认值（如 strftime(...)），故先加可空列再回填。
ALTER TABLE users ADD COLUMN updated_at TEXT;
UPDATE users SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE updated_at IS NULL;

ALTER TABLE credentials ADD COLUMN phone TEXT;

CREATE TABLE IF NOT EXISTS auth_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose    TEXT NOT NULL CHECK (purpose IN ('email_verify', 'password_reset')),
    token_hash TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_tokens_hash ON auth_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_user ON auth_tokens (user_id, purpose, created_at DESC);
