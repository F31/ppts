import { useCallback, useEffect, useState } from 'react';
import {
  createGateway,
  deleteGateway,
  listGateways,
  setDefaultGateway,
  testGateway,
  updateGateway,
  type ClientIdentity,
  type GatewayTestResult,
  type ModelGateway
} from './api';
import { useDialogA11y } from './a11y';
import { gatewayHealth, gatewayKey, healthKeySuffix, serviceKeySuffix, serviceState } from './gatewayState';
import { useI18n } from './i18n';

type FormState = {
  kind: 'tts' | 'llm';
  name: string;
  provider: string;
  baseUrl: string;
  apiKey: string;
  model: string;
  visionModel: string;
  voice: string;
  isDefault: boolean;
};

const defaultProvider = 'openai_compatible';

const emptyForm: FormState = {
  kind: 'tts',
  name: '',
  provider: defaultProvider,
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
  onClose,
  inline = true
}: {
  identity: ClientIdentity;
  onSaved?: () => void | Promise<void>;
  onClose?: () => void;
  inline?: boolean;
}) {
  const { t } = useI18n();
  const [gateways, setGateways] = useState<ModelGateway[]>([]);
  const [loading, setLoading] = useState(true);
  const dialogRef = useDialogA11y<HTMLDivElement>(() => onClose?.());
  const [error, setError] = useState('');
  const [form, setForm] = useState<FormState | null>(null);
  const [editing, setEditing] = useState<{ name: string; version: number } | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, GatewayTestResult>>({});

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setGateways(await listGateways(identity));
    } catch (err) {
      const msg = err instanceof Error ? err.message : '';
      // 网关功能在后端未启用（缺 AES 密钥）时返回 503 feature_disabled，给出友好提示而非原始报错。
      if (msg.includes('feature_disabled') || msg.includes('model gateway disabled')) {
        setError(t('gateway.featureDisabled'));
      } else {
        setError(msg || t('gateway.errLoad'));
      }
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const submit = async () => {
    if (!form) return;
    setError('');
    try {
      if (editing) {
        await updateGateway(identity, editing.name, {
          kind: form.kind,
          version: editing.version,
          provider: form.provider || undefined,
          baseUrl: form.baseUrl,
          apiKey: form.apiKey,
          model: form.model,
          visionModel: form.visionModel || undefined,
          voice: form.voice || undefined,
          isDefault: form.isDefault
        });
      } else {
        await createGateway(identity, {
          kind: form.kind,
          name: form.name || 'gateway',
          provider: form.provider || undefined,
          baseUrl: form.baseUrl,
          apiKey: form.apiKey,
          model: form.model,
          visionModel: form.visionModel || undefined,
          voice: form.voice || undefined,
          isDefault: form.isDefault
        });
      }
      setForm(null);
      setEditing(null);
      await load();
      await onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('gateway.errSave'));
    }
  };

  const startEdit = (gw: ModelGateway) => {
    setEditing({ name: gw.name, version: gw.version });
    setForm({
      kind: gw.kind,
      name: gw.name,
      provider: gw.provider,
      baseUrl: gw.baseUrl,
      apiKey: '',
      model: gw.model,
      visionModel: gw.visionModel,
      voice: gw.voice,
      isDefault: gw.isDefault
    });
  };

  const runTest = async (gw: ModelGateway) => {
    const key = gatewayKey(gw);
    setTesting(key);
    try {
      const result = await testGateway(identity, gw.name, gw.kind);
      setTestResult((current) => ({ ...current, [key]: result }));
    } catch (err) {
      setTestResult((current) => ({
        ...current,
        [key]: { ok: false, latencyMs: 0, error: err instanceof Error ? err.message : t('gateway.errTest') }
      }));
    } finally {
      setTesting(null);
    }
  };

  const remove = async (gw: ModelGateway) => {
    if (!window.confirm(t('gateway.deleteConfirm', { name: gw.name, kind: gw.kind }))) return;
    try {
      await deleteGateway(identity, gw.name, gw.kind);
      await load();
      await onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('gateway.errDelete'));
    }
  };

  // 语音合成服务的总体可用性（A23）：未配置 / 当前不可用 / 未检测 / 可用。
  const ttsState = serviceState(gateways, 'tts', testResult);

  const content = (
    <>
      <header>
        <div>
          <span className="eyebrow">{t('gateway.eyebrow')}</span>
          <h2>{t('gateway.title')}</h2>
        </div>
        {onClose && !inline && (
          <button type="button" onClick={onClose}>
            {t('common.close')}
          </button>
        )}
      </header>
        {error && <p className="form-error">{error}</p>}
        {!loading && !error && (
          <div className={`service-banner ${ttsState}`} role="status">
            <strong>{t('gateway.serviceTitle')}</strong>
            <span>{t(`gateway.service${serviceKeySuffix[ttsState]}`)}</span>
          </div>
        )}
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : (
          <ul className="gateway-list">
            {gateways.length === 0 && <li className="empty-state">{t('gateway.empty')}</li>}
            {gateways.map((gw) => {
              const key = gatewayKey(gw);
              const result = testResult[key];
              const health = gatewayHealth(gw, result);
              return (
                <li key={key} className="gateway-item">
                  <div className="gateway-head">
                    <strong>{gw.name}</strong>
                    <span className={`kind-tag ${gw.kind}`}>{gw.kind === 'tts' ? t('gateway.kindTts') : t('gateway.kindLlm')}</span>
                    {gw.isDefault && <span className="default-tag">{t('gateway.default')}</span>}
                    <span className={`health-tag ${health}`}>{t(`gateway.health${healthKeySuffix[health]}`)}</span>
                    <small>
                      {gw.baseUrl} · {gw.model}
                      {gw.visionModel ? ` · ${gw.visionModel}` : ''}
                    </small>
                    <small>
                      {t('gateway.providerLabel')}: {gw.provider || defaultProvider}
                    </small>
                    <small>{gw.hasKey ? t('gateway.keyMasked', { masked: gw.keyMasked }) : t('gateway.noKey')}</small>
                  </div>
                  <div className="draft-actions">
                    <button type="button" disabled={testing === key} onClick={() => void runTest(gw)}>
                      {testing === key ? t('gateway.testing') : health === 'untested' ? t('gateway.testNow') : t('gateway.test')}
                    </button>
                    <button type="button" onClick={() => startEdit(gw)}>
                      {t('common.edit')}
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
                            setError(err instanceof Error ? err.message : t('gateway.errSetDefault'));
                          }
                        }}
                      >
                        {t('gateway.setDefault')}
                      </button>
                    )}
                    <button type="button" className="danger" onClick={() => void remove(gw)}>
                      {t('common.delete')}
                    </button>
                  </div>
                  {result && (
                    <>
                      <p className={`test-result ${result.ok ? 'ok' : 'fail'}`}>
                        {result.ok ? t('gateway.testOk', { ms: result.latencyMs }) : t('gateway.testFail', { error: result.error ?? '' })}
                      </p>
                      {!result.ok && <p className="test-fix">{t('gateway.fixHint')}</p>}
                    </>
                  )}
                  {!result && gw.enabled && gw.hasKey && (
                    <p className="test-hint">{t('gateway.untestedNote')}</p>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {form === null ? (
          <div className="draft-actions">
            <button
              type="button"
              onClick={() => {
                setEditing(null);
                setForm({ ...emptyForm });
              }}
            >
              {t('gateway.new')}
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
                {t('gateway.typeLabel')}
                <select
                  value={form.kind}
                  disabled={!!editing}
                  onChange={(e) => {
                    const kind = e.target.value as 'tts' | 'llm';
                    setForm((f) => f && {
                      ...f,
                      kind,
                      model: kind === 'tts' ? 'FunAudioLLM/CosyVoice2-0.5B' : 'Qwen/Qwen2.5-7B-Instruct'
                    });
                  }}
                >
                  <option value="tts">{t('gateway.kindTtsFull')}</option>
                  <option value="llm">{t('gateway.kindLlmFull')}</option>
                </select>
              </label>
              <label>
                {t('gateway.nameLabel')}
                <input
                  value={form.name}
                  placeholder={t('gateway.namePlaceholder')}
                  disabled={!!editing}
                  title={editing ? t('gateway.immutableWhenEditing') : ''}
                  onChange={(e) => setForm((f) => f && { ...f, name: e.target.value })}
                  required
                />
              </label>
            </div>
            <div className="form-row">
              <label>
                {t('gateway.providerLabel')}
                <input
                  value={form.provider}
                  placeholder={defaultProvider}
                  onChange={(e) => setForm((f) => f && { ...f, provider: e.target.value })}
                />
              </label>
              <label>
                {t('gateway.baseUrlLabel')}
                <input
                  value={form.baseUrl}
                  placeholder="https://api.siliconflow.cn"
                  onChange={(e) => setForm((f) => f && { ...f, baseUrl: e.target.value })}
                  required
                />
              </label>
            </div>
            <div className="form-row">
              <label>
                {t('gateway.apiKeyLabel')}
                <input
                  type="password"
                  value={form.apiKey}
                  placeholder="sk-…"
                  onChange={(e) => setForm((f) => f && { ...f, apiKey: e.target.value })}
                />
              </label>
              <label>
                {t('gateway.modelLabel')}
                <input
                  value={form.model}
                  onChange={(e) => setForm((f) => f && { ...f, model: e.target.value })}
                  required
                />
              </label>
            </div>
            <div className="form-row">
              {form.kind === 'llm' ? (
                <label>
                  {t('gateway.visionLabel')}
                  <input
                    value={form.visionModel}
                    onChange={(e) => setForm((f) => f && { ...f, visionModel: e.target.value })}
                  />
                </label>
              ) : (
                <label>
                  {t('gateway.voiceLabel')}
                  <input
                    value={form.voice}
                    placeholder={t('gateway.voicePlaceholder')}
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
              {t('gateway.setAsDefault')}
            </label>
            <div className="draft-actions">
              <button type="submit">{editing ? t('gateway.saveEdit') : t('gateway.save')}</button>
              <button
                type="button"
                onClick={() => {
                  setForm(null);
                  setEditing(null);
                }}
              >
                {t('common.cancel')}
              </button>
            </div>
          </form>
        )}
    </>
  );

  if (inline) {
    return <section className="panel gateway-panel">{content}</section>;
  }
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label={t('gateway.eyebrow')} ref={dialogRef}>
      <section className="modal-card gateway-panel">{content}</section>
    </div>
  );
}
