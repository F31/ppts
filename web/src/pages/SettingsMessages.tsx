import { useCallback, useEffect, useState } from 'react';
import {
  getMessageChannels,
  saveEmailChannel,
  saveSmsChannel,
  testEmailChannel,
  type ClientIdentity,
  type MessageChannels
} from '../api';
import { describeApiError } from '../apiError';
import { useI18n } from '../i18n';

type Tab = 'email' | 'sms';
type Scope = 'tenant' | 'platform';

// SettingsMessages 是「消息服务」设置页：发件箱服务配置 + 短信网关配置两个 Tab。
// 发件箱配置可选平台默认（仅运营商），用于新用户注册/验证邮件兜底。
export function SettingsMessages({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [tab, setTab] = useState<Tab>('email');
  const [channels, setChannels] = useState<MessageChannels | null>(null);
  const [loadError, setLoadError] = useState('');

  const load = useCallback(async () => {
    setLoadError('');
    try {
      setChannels(await getMessageChannels(identity));
    } catch (err) {
      setLoadError(describeApiError(err, t('messages.loadFailed'), t));
    }
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('messages.eyebrow')}</span>
          <h1>{t('messages.title')}</h1>
          <small className="page-sub">{t('messages.subtitle')}</small>
        </div>
      </section>
      <div className="tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'email'}
          className={`tab ${tab === 'email' ? 'selected' : ''}`}
          onClick={() => setTab('email')}
        >
          {t('messages.tabEmail')}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'sms'}
          className={`tab ${tab === 'sms' ? 'selected' : ''}`}
          onClick={() => setTab('sms')}
        >
          {t('messages.tabSms')}
        </button>
      </div>
      {loadError && <p className="form-error" role="alert">{loadError}</p>}
      {channels &&
        (tab === 'email' ? (
          <EmailChannel identity={identity} channels={channels} onSaved={load} />
        ) : (
          <SmsChannel identity={identity} channels={channels} onSaved={load} />
        ))}
    </div>
  );
}

function EmailChannel({
  identity,
  channels,
  onSaved
}: {
  identity: ClientIdentity;
  channels: MessageChannels;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const isOperator = channels.is_operator;
  const [scope, setScope] = useState<Scope>('tenant');
  const [enabled, setEnabled] = useState(false);
  const [host, setHost] = useState('');
  const [port, setPort] = useState('587');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [fromAddress, setFromAddress] = useState('');
  const [fromName, setFromName] = useState('');
  const [tlsMode, setTlsMode] = useState('starttls');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState(false);
  const [testTo, setTestTo] = useState('');
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; error?: string } | null>(null);

  // 作用域切换时把对应已保存配置填入表单（运营商可切到平台默认）。
  useEffect(() => {
    const cfg = scope === 'platform' ? channels.platform_email : channels.email;
    setEnabled(cfg?.enabled ?? false);
    setHost(cfg?.host ?? '');
    setPort(String(cfg?.port ?? 587));
    setUsername(cfg?.username ?? '');
    setPassword('');
    setFromAddress(cfg?.from_address ?? '');
    setFromName(cfg?.from_name ?? '');
    setTlsMode(cfg?.tls_mode || 'starttls');
    setSaved(false);
    setError('');
    setTestResult(null);
  }, [scope, channels]);

  const save = async () => {
    setSaving(true);
    setError('');
    setSaved(false);
    try {
      await saveEmailChannel(identity, {
        enabled,
        host,
        port: Number(port) || 0,
        username,
        from_address: fromAddress,
        from_name: fromName,
        tls_mode: tlsMode,
        password: password || undefined,
        platform_default: scope === 'platform'
      });
      setPassword('');
      setSaved(true);
      onSaved();
    } catch (err) {
      setError(describeApiError(err, t('messages.saveFailed'), t));
    } finally {
      setSaving(false);
    }
  };

  const sendTest = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const res = await testEmailChannel(identity, testTo.trim(), scope === 'platform');
      setTestResult(res);
    } catch (err) {
      setTestResult({ ok: false, error: describeApiError(err, t('messages.testFailed'), t) });
    } finally {
      setTesting(false);
    }
  };

  const current = scope === 'platform' ? channels.platform_email : channels.email;

  return (
    <section className="panel">
      {isOperator && (
        <div className="field">
          <span className="field-label">{t('messages.scope')}</span>
          <div className="radio-row">
            <label>
              <input type="radio" checked={scope === 'tenant'} onChange={() => setScope('tenant')} /> {t('messages.scopeTenant')}
            </label>
            <label>
              <input type="radio" checked={scope === 'platform'} onChange={() => setScope('platform')} /> {t('messages.scopePlatform')}
            </label>
          </div>
          {scope === 'platform' && <small className="field-hint">{t('messages.scopePlatformHint')}</small>}
        </div>
      )}
      <label className="checkbox-row">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} /> {t('messages.enabled')}
      </label>
      <div className="field-grid">
        <label className="field">
          <span className="field-label">SMTP {t('messages.host')}</span>
          <input value={host} onChange={(e) => setHost(e.currentTarget.value)} placeholder="smtp.example.com" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.port')}</span>
          <input value={port} onChange={(e) => setPort(e.currentTarget.value)} placeholder="587" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.username')}</span>
          <input value={username} onChange={(e) => setUsername(e.currentTarget.value)} autoComplete="off" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.password')}</span>
          <input
            type="password"
            value={password}
            autoComplete="new-password"
            placeholder={current?.has_password ? current.password_masked : ''}
            onChange={(e) => setPassword(e.currentTarget.value)}
          />
          <small className="field-hint">{t('messages.passwordHint')}</small>
        </label>
        <label className="field">
          <span className="field-label">{t('messages.fromName')}</span>
          <input value={fromName} onChange={(e) => setFromName(e.currentTarget.value)} placeholder="PPTS" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.fromAddress')}</span>
          <input value={fromAddress} onChange={(e) => setFromAddress(e.currentTarget.value)} placeholder="no-reply@example.com" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.tlsMode')}</span>
          <select value={tlsMode} onChange={(e) => setTlsMode(e.currentTarget.value)}>
            <option value="starttls">STARTTLS (587)</option>
            <option value="tls">TLS (465)</option>
            <option value="none">None (25)</option>
          </select>
        </label>
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      {saved && <p className="form-success" role="status">{t('messages.saved')}</p>}
      <div className="actions-row">
        <button type="button" className="primary" disabled={saving} onClick={() => void save()}>
          {saving ? t('messages.saving') : t('messages.save')}
        </button>
      </div>

      <hr className="divider" />
      <h3>{t('messages.testTitle')}</h3>
      <small className="field-hint">{t('messages.testHint')}</small>
      <div className="actions-row">
        <input value={testTo} placeholder={t('messages.testTo')} onChange={(e) => setTestTo(e.currentTarget.value)} />
        <button type="button" disabled={testing || !testTo.trim()} onClick={() => void sendTest()}>
          {testing ? t('messages.testing') : t('messages.test')}
        </button>
      </div>
      {testResult && (
        <p className={testResult.ok ? 'form-success' : 'form-error'} role="status">
          {testResult.ok ? t('messages.testOk') : `${t('messages.testFail')}: ${testResult.error ?? ''}`}
        </p>
      )}
    </section>
  );
}

function SmsChannel({
  identity,
  channels,
  onSaved
}: {
  identity: ClientIdentity;
  channels: MessageChannels;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const cfg = channels.sms;
  const [enabled, setEnabled] = useState(cfg?.enabled ?? false);
  const [provider, setProvider] = useState(cfg?.provider ?? '');
  const [endpoint, setEndpoint] = useState(cfg?.endpoint ?? '');
  const [signName, setSignName] = useState(cfg?.sign_name ?? '');
  const [templateCode, setTemplateCode] = useState(cfg?.template_code ?? '');
  const [accessKeyId, setAccessKeyId] = useState(cfg?.access_key_id ?? '');
  const [accessKeySecret, setAccessKeySecret] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState(false);

  const save = async () => {
    setSaving(true);
    setError('');
    setSaved(false);
    try {
      await saveSmsChannel(identity, {
        enabled,
        provider,
        endpoint,
        sign_name: signName,
        template_code: templateCode,
        access_key_id: accessKeyId,
        access_key_secret: accessKeySecret || undefined
      });
      setAccessKeySecret('');
      setSaved(true);
      onSaved();
    } catch (err) {
      setError(describeApiError(err, t('messages.saveFailed'), t));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="panel">
      <p className="notice">{t('messages.smsPending')}</p>
      <label className="checkbox-row">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} /> {t('messages.enabled')}
      </label>
      <div className="field-grid">
        <label className="field">
          <span className="field-label">{t('messages.provider')}</span>
          <input value={provider} onChange={(e) => setProvider(e.currentTarget.value)} placeholder="aliyun" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.endpoint')}</span>
          <input value={endpoint} onChange={(e) => setEndpoint(e.currentTarget.value)} placeholder="dysmsapi.aliyuncs.com" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.signName')}</span>
          <input value={signName} onChange={(e) => setSignName(e.currentTarget.value)} />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.templateCode')}</span>
          <input value={templateCode} onChange={(e) => setTemplateCode(e.currentTarget.value)} />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.accessKeyId')}</span>
          <input value={accessKeyId} onChange={(e) => setAccessKeyId(e.currentTarget.value)} autoComplete="off" />
        </label>
        <label className="field">
          <span className="field-label">{t('messages.accessKeySecret')}</span>
          <input
            type="password"
            value={accessKeySecret}
            autoComplete="new-password"
            placeholder={cfg?.has_secret ? cfg.secret_masked : ''}
            onChange={(e) => setAccessKeySecret(e.currentTarget.value)}
          />
          <small className="field-hint">{t('messages.passwordHint')}</small>
        </label>
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      {saved && <p className="form-success" role="status">{t('messages.saved')}</p>}
      <div className="actions-row">
        <button type="button" className="primary" disabled={saving} onClick={() => void save()}>
          {saving ? t('messages.saving') : t('messages.save')}
        </button>
      </div>
    </section>
  );
}
