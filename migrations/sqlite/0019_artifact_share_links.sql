-- 成品级分享链接（M6-3，2026-10-29；与 pg 0052 对齐）
--
-- 单租户 SQLite profile：无 RLS，隔离靠 tenant_id 列 + 调用方上下文（与 0018 一致）。
-- 因此「撤销/过期立即生效」在本 profile 下**完全**依赖查询条件里的有效性子句
-- （SQLite 没有 anon_read 策略兜底）。该子句在 internal/artifact/share_link.go 里
-- 抽成一个常量，PG 与 SQLite 共用，避免这边少一个条件而 PG 侧测试全绿的漂移。

CREATE TABLE IF NOT EXISTS artifact_share_links (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL,
    -- 锚点：成品，不是项目。ON DELETE CASCADE 保证成品删除后链接随行消失。
    artifact_id        TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    -- 冗余列：只为「按项目列出分享」与审计可读；不参与内容定位。
    project_id         TEXT NOT NULL,
    -- 创建链接那一刻成品指向的对象键与内容哈希（快照）。理由见 pg 0052：
    -- artifacts 的 upsert 会原地改写 object_key，只绑 artifact_id 不够。
    object_key         TEXT NOT NULL,
    content_hash       TEXT NOT NULL,
    token              TEXT NOT NULL UNIQUE,
    access_mode        TEXT NOT NULL DEFAULT 'view_only'
                       CHECK (access_mode IN ('view_only', 'view_and_comment')),
    password_hash      TEXT,
    password_protected INTEGER NOT NULL DEFAULT 0,
    expires_at         TEXT,
    revoked            INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_accessed_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_artifact_share_links_artifact ON artifact_share_links (artifact_id);
CREATE INDEX IF NOT EXISTS idx_artifact_share_links_project ON artifact_share_links (project_id);
CREATE INDEX IF NOT EXISTS idx_artifact_share_links_tenant ON artifact_share_links (tenant_id);
