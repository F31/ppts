-- 项目级语音属性（语音模型 / 音色 / 语速）持久化（SQLite 单租户）。
CREATE TABLE IF NOT EXISTS project_voice_settings (
    tenant_id    TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    model        TEXT NOT NULL DEFAULT '',
    voice        TEXT NOT NULL DEFAULT '',
    rate_percent INTEGER NOT NULL DEFAULT 100,
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (tenant_id, project_id)
);
