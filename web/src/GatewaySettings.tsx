import { useCallback, useEffect, useState } from 'react';
import {
  createGateway,
  deleteGateway,
  listGateways,
  setDefaultGateway,
  testGateway,
  type ClientIdentity,
  type GatewayTestResult,
  type ModelGateway
} from './api';

type FormState = {
  kind: 'tts' | 'llm';
  name: string;
  baseUrl: string;
  apiKey: string;
  model: string;
  visionModel: string;
  voice: string;
  isDefault: boolean;
};

const emptyForm: FormState = {
  kind: 'tts',
  name: '',
  baseUrl: 'https://api.siliconflow.cn',
  apiKey: '',
  model: 'FunAudioLLM/CosyVoice2-0.5B',
  visionModel: 'Qwen/Qwen3-VL-8B-Instruct',
  voice: '',
  isDefault: true
};

export function GatewaySettings({
  identity,
  onSaved,
  onClose
}: {
  identity: ClientIdentity;
  onSaved?: () => void | Promise<void>;
  onClose: () => void;
}) {
  const [gateways, setGateways] = useState<ModelGateway[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [form, setForm] = useState<FormState | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, GatewayTestResult>>({});

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setGateways(await listGateways(identity));
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载网关失败');
    } finally {
      setLoading(false);
    }
  }, [identity]);

  useEffect(() => {
    void load();
  }, [load]);

  const submit = async () => {
    if (!form) return;
    setError('');
    try {
      await createGateway(identity, {
        kind: form.kind,
        name: form.name || 'gateway',
        baseUrl: form.baseUrl,
        apiKey: form.apiKey,
        model: form.model,
        visionModel: form.visionModel || undefined,
        voice: form.voice || undefined,
        isDefault: form.isDefault
      });
      setForm(null);
      await load();
      await onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存失败');
    }
  };

  const runTest = async (gw: ModelGateway) => {
    const key = `${gw.kind}:${gw.name}`;
    setTesting(key);
    try {
      const result = await testGateway(identity, gw.name, gw.kind);
      setTestResult((current) => ({ ...current, [key]: result }));
    } catch (err) {
      setTestResult((current) => ({
        ...current,
        [key]: { ok: false, latencyMs: 0, error: err instanceof Error ? err.message : '探活失败' }
      }));
    } finally {
      setTesting(null);
    }
  };

  const remove = async (gw: ModelGateway) => {
    if (!window.confirm(`删除网关 ${gw.name}（${gw.kind}）？`)) return;
    try {
      await deleteGateway(identity, gw.name, gw.kind);
      await load();
      await onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败');
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-label="模型网关配置">
      <section className="modal-card gateway-panel">
        <header>
          <div>
            <span className="eyebrow">模型网关</span>
            <h2>TTS / LLM 接入配置</h2>
          </div>
          <button type="button" onClick={onClose}>
            关闭
          </button>
        </header>
        {error && <p className="form-error">{error}</p>}
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : (
          <ul className="gateway-list">
            {gateways.length === 0 && <li className="empty-state">尚无网关：点击下方"新建网关"接入 TTS / LLM 供应商。</li>}
            {gateways.map((gw) => {
              const key = `${gw.kind}:${gw.name}`;
              const result = testResult[key];
              return (
                <li key={key} className="gateway-item">
                  <div className="gateway-head">
                    <strong>{gw.name}</strong>
                    <span className={`kind-tag ${gw.kind}`}>{gw.kind === 'tts' ? '语音合成' : '文本/视觉'}</span>
                    {gw.isDefault && <span className="default-tag">默认</span>}
                    {!gw.enabled && <span className="disabled-tag">已停用</span>}
                    <small>
                      {gw.baseUrl} · {gw.model}
                      {gw.visionModel ? ` · ${gw.visionModel}` : ''}
                    </small>
                    <small>{gw.hasKey ? `key: ${gw.keyMasked}` : '未配置 key'}</small>
                  </div>
                  <div className="draft-actions">
                    <button type="button" disabled={testing === key} onClick={() => void runTest(gw)}>
                      {testing === key ? '测试中…' : '测试连接'}
                    </button>
                    {!gw.isDefault && (
                      <button
                        type="button"
                        onClick={async () => {
                          try {
                            await setDefaultGateway(identity, gw.name, gw.kind);
                            await load();
                            await onSaved?.();
                          } catch (err) {
                            setError(err instanceof Error ? err.message : '设置默认失败');
                          }
                        }}
                      >
                        设为默认
                      </button>
                    )}
                    <button type="button" className="danger" onClick={() => void remove(gw)}>
                      删除
                    </button>
                  </div>
                  {result && (
                    <p className={`test-result ${result.ok ? 'ok' : 'fail'}`}>
                      {result.ok ? `连通正常，延迟 ${result.latencyMs}ms` : `连通失败：${result.error}`}
                    </p>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {form === null ? (
          <div className="draft-actions">
            <button type="button" onClick={() => setForm({ ...emptyForm })}>
              新建网关
            </button>
          </div>
        ) : (
          <form
            className="gateway-form"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <div className="form-row">
              <label>
                类型
                <select
                  value={form.kind}
                  onChange={(e) => {
                    const kind = e.target.value as 'tts' | 'llm';
                    setForm((f) => f && {
                      ...f,
                      kind,
                      model: kind === 'tts' ? 'FunAudioLLM/CosyVoice2-0.5B' : 'Qwen/Qwen2.5-7B-Instruct'
                    });
                  }}
                >
                  <option value="tts">TTS 语音合成</option>
                  <option value="llm">LLM 文本/视觉</option>
                </select>
              </label>
              <label>
                名称
                <input
                  value={form.name}
                  placeholder="如 siliconflow"
                  onChange={(e) => setForm((f) => f && { ...f, name: e.target.value })}
                  required
                />
              </label>
            </div>
            <div className="form-row">
              <label>
                网关地址（OpenAI 兼容）
                <input
                  value={form.baseUrl}
                  placeholder="https://api.siliconflow.cn"
                  onChange={(e) => setForm((f) => f && { ...f, baseUrl: e.target.value })}
                  required
                />
              </label>
              <label>
                API Key（留空=不变，新建必填）
                <input
                  type="password"
                  value={form.apiKey}
                  placeholder="sk-…"
                  onChange={(e) => setForm((f) => f && { ...f, apiKey: e.target.value })}
                />
              </label>
            </div>
            <div className="form-row">
              <label>
                模型
                <input
                  value={form.model}
                  onChange={(e) => setForm((f) => f && { ...f, model: e.target.value })}
                  required
                />
              </label>
              {form.kind === 'llm' ? (
                <label>
                  视觉模型（可选）
                  <input
                    value={form.visionModel}
                    onChange={(e) => setForm((f) => f && { ...f, visionModel: e.target.value })}
                  />
                </label>
              ) : (
                <label>
                  音色（可选，格式 模型:音色）
                  <input
                    value={form.voice}
                    onChange={(e) => setForm((f) => f && { ...f, voice: e.target.value })}
                  />
                </label>
              )}
            </div>
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={form.isDefault}
                onChange={(e) => setForm((f) => f && { ...f, isDefault: e.target.checked })}
              />
              设为该类型的默认网关
            </label>
            <div className="draft-actions">
              <button type="submit">保存</button>
              <button type="button" onClick={() => setForm(null)}>
                取消
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
