import { useState } from 'react';
import type { ClientIdentity } from '../api';
import { oidcConfigured, isDevIdentityEnabled, startOIDCLogin } from '../auth';
import { useI18n } from '../i18n';
import { useSession } from '../session';

const defaultTenantId = '00000000-0000-0000-0000-000000000000';

export function Login() {
  const { loginDev, loginOIDC } = useSession();
  const { t } = useI18n();
  const [tenantId, setTenantId] = useState(defaultTenantId);
  const [userId, setUserId] = useState('dev-user');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submitDev = async () => {
    if (submitting) return;
    setSubmitting(true);
    setError('');
    const trimmedTenant = tenantId.trim();
    const trimmedUser = userId.trim();
    if (!trimmedTenant || !trimmedUser) {
      setError(t('login.required'));
      setSubmitting(false);
      return;
    }
    const identity: ClientIdentity = { tenantId: trimmedTenant, userId: trimmedUser };
    loginDev(identity);
    setSubmitting(false);
  };

  const onOIDC = async () => {
    if (submitting) return;
    setSubmitting(true);
    setError('');
    try {
      await startOIDCLogin();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('login.oidcFailed'));
      setSubmitting(false);
    }
  };

  const showOIDC = oidcConfigured();
  const showDev = isDevIdentityEnabled();

  return (
    <main className="login-page">
      <aside className="login-brand">
        <span className="brand-mark">{t('shell.brand')}</span>
        <h1>{t('login.tagline')}</h1>
        <p>{t('login.flow')}</p>
      </aside>
      <section className="login-card">
        <span className="eyebrow">{t('login.eyebrow')}</span>
        <h2>{t('login.title')}</h2>
        {showOIDC && (
          <>
            <button type="button" className="primary-login" disabled={submitting} onClick={() => void onOIDC()}>
              {submitting ? t('login.redirecting') : t('login.oidc')}
            </button>
            <div className="login-divider">
              <span>{t('login.or')}</span>
            </div>
          </>
        )}
        {showDev ? (
          <>
            <label>
              {t('login.tenantId')}
              <input value={tenantId} onChange={(e) => setTenantId(e.currentTarget.value)} placeholder={defaultTenantId} />
            </label>
            <label>
              {t('login.userId')}
              <input value={userId} onChange={(e) => setUserId(e.currentTarget.value)} placeholder="dev-user" />
            </label>
            <button type="button" className="dev-login" disabled={submitting} onClick={() => void submitDev()}>
              {submitting ? t('login.loggingIn') : t('login.dev')}
            </button>
            <p className="dev-login-note">{t('login.devNote')}</p>
          </>
        ) : !showOIDC ? (
          <p className="login-unconfigured">{t('login.unconfigured')}</p>
        ) : null}
        {error && <p className="form-error">{error}</p>}
      </section>
    </main>
  );
}