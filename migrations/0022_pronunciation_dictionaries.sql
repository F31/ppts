-- G2-4 发音词典：逐词/短语替换规则，解决专有名词（CUDA、Kubernetes、MySQL 等）
-- 误读。词典按租户作用域，合成时应用到 spoken_text 后再送 TTS。
CREATE TABLE IF NOT EXISTS pronunciation_dictionaries (
    id         uuid PRIMARY KEY,
    tenant_id  uuid NOT NULL,
    name       text NOT NULL,
    rules      jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pronunciation_dictionaries_tenant
    ON pronunciation_dictionaries (tenant_id);

-- narration_segments 可选词典覆盖：若为 NULL 则使用租户默认词典。
ALTER TABLE narration_segments
  ADD COLUMN IF NOT EXISTS dictionary_id uuid;
