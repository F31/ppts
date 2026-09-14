import type { ClientIdentity } from '../api';
import { GatewaySettings } from '../GatewaySettings';

export function SettingsModels({ identity }: { identity: ClientIdentity }) {
  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">设置 · 模型服务</span>
          <h1>模型服务</h1>
          <small className="page-sub">
            TTS / LLM 供应商配置（endpoint、API Key、模型、音色）。配置变更后 Worker 最多 30 秒热生效。
          </small>
        </div>
      </section>
      <GatewaySettings identity={identity} inline />
    </div>
  );
}