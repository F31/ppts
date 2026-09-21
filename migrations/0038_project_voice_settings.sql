-- 项目级语音属性（语音模型 / 音色 / 语速）持久化：编辑器「语音属性」弹窗保存时写入。
-- tenant 隔离，遵循 0006 RLS 纵深防御。

CREATE TABLE IF NOT EXISTS project_voice_settings (
    tenant_id    uuid NOT NULL,
    project_id   uuid NOT NULL,
    model        text NOT NULL DEFAULT '',
    voice        text NOT NULL DEFAULT '',
    rate_percent integer NOT NULL DEFAULT 100,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_id)
);

ALTER TABLE project_voice_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_voice_settings FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON project_voice_settings;
CREATE POLICY tenant_isolation ON project_voice_settings
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON project_voice_settings TO ppts_app;
