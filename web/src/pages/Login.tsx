import { useEffect, useState } from 'react';
import type { ClientIdentity } from '../api';
import { oidcConfigured, isDevIdentityEnabled, startOIDCLogin } from '../auth';
import { getAuthConfig, loginEmail, registerEmail } from '../api';
import { useI18n } from '../i18n';
import { useSession } from '../session';
import { describeApiError } from '../apiError';

const defaultTenantId = '00000000-0000-0000-0000-000000000000';
type EmailMode = 'signin' | 'register';

export function Login() {
  const { loginDev, loginOIDC, loginEmail: loginEmailSession } = useSession();
  const { t, lang, toggle } = useI18n();
  const [tenantId, setTenantId] = useState(defaultTenantId);
  const [userId, setUserId] = useState('dev-user');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  // 邮箱能力探测（B5 门控：能力未配置则隐藏入口，不做假登录）。
  const [emailEnabled, setEmailEnabled] = useState<boolean | null>(null);
  const [emailMode, setEmailMode] = useState<EmailMode>('signin');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  // 注册时采集的可选档案字段（成员列表富字段展示）。
  const [regUsername, setRegUsername] = useState('');
  const [regFullName, setRegFullName] = useState('');
  const [regGender, setRegGender] = useState('');
  const [regBirthDate, setRegBirthDate] = useState('');
  const [regPhone, setRegPhone] = useState('');

  useEffect(() => {
    let cancelled = false;
    getAuthConfig()
      .then((cfg) => {
        if (!cancelled) setEmailEnabled(cfg.email_password);
      })
      .catch(() => {
        if (!cancelled) setEmailEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

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

  const submitEmail = async () => {
    if (submitting) return;
    setSubmitting(true);
    setError('');
    const trimmedEmail = email.trim();
    if (!trimmedEmail || !password) {
      setError(t('login.emailRequired'));
      setSubmitting(false);
      return;
    }
    try {
      const res =
        emailMode === 'register'
          ? await registerEmail({
              email: trimmedEmail,
              password,
              username: regUsername,
              fullName: regFullName,
              gender: regGender,
              birthDate: regBirthDate,
              phone: regPhone,
            })
          : await loginEmail({ email: trimmedEmail, password });
      loginEmailSession({ tenantId: res.tenant_id, userId: res.user_id, accessToken: res.access_token, account: res.account, tenantName: res.tenant_name });
    } catch (err) {
      setError(
        describeApiError(
          err,
          emailMode === 'register' ? t('login.registerFailed') : t('login.invalidCredentials'),
          t
        )
      );
      setSubmitting(false);
    }
  };

  const showOIDC = oidcConfigured();
  const showDev = isDevIdentityEnabled();

  return (
    <main className="login-page">
      <button type="button" className="lang-toggle login-lang" onClick={toggle} title={t('shell.languageToggle')}>
        {lang === 'zh' ? t('lang.en') : t('lang.zh')}
      </button>
      <aside className="login-brand">
        <span className="brand-mark">{t('shell.brand')}</span>
        <h1>{t('login.tagline')}</h1>
        <p>{t('login.flow')}</p>
      </aside>
      <section className="login-card">
        <span className="eyebrow">{t('login.eyebrow')}</span>
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
        {emailEnabled === true && (
          <div className="email-auth">
            <label>
              {t('login.email')}
              <input
                type="text"
                value={email}
                autoComplete="username"
                placeholder={t('login.accountPlaceholder')}
                onChange={(e) => setEmail(e.currentTarget.value)}
              />
            </label>
            <label>
              {t('login.password')}
              <input
                type="password"
                value={password}
                autoComplete={emailMode === 'register' ? 'new-password' : 'current-password'}
                onChange={(e) => setPassword(e.currentTarget.value)}
              />
            </label>
            {emailMode === 'register' && (
              <div className="register-profile">
                <label>
                  {t('login.username')}
                  <input value={regUsername} onChange={(e) => setRegUsername(e.currentTarget.value)} placeholder={t('login.usernamePlaceholder')} />
                </label>
                <label>
                  {t('login.fullName')}
                  <input value={regFullName} onChange={(e) => setRegFullName(e.currentTarget.value)} placeholder={t('login.fullNamePlaceholder')} />
                </label>
                <label>
                  {t('login.gender')}
                  <select value={regGender} onChange={(e) => setRegGender(e.currentTarget.value)}>
                    <option value="">—</option>
                    <option value="male">{t('members.gender.male')}</option>
                    <option value="female">{t('members.gender.female')}</option>
                    <option value="other">{t('members.gender.other')}</option>
                    <option value="unknown">{t('members.gender.unknown')}</option>
                  </select>
                </label>
                <label>
                  {t('login.birthDate')}
                  <input type="month" value={regBirthDate} onChange={(e) => setRegBirthDate(e.currentTarget.value)} />
                </label>
                <label>
                  {t('login.phone')}
                  <input value={regPhone} onChange={(e) => setRegPhone(e.currentTarget.value)} placeholder={t('login.phonePlaceholder')} />
                </label>
              </div>
            )}
            <button type="button" className="primary-login" disabled={submitting} onClick={() => void submitEmail()}>
              {submitting ? t('login.submitting') : emailMode === 'register' ? t('login.register') : t('login.signIn')}
            </button>
            <p className="email-switch">
              {emailMode === 'register' ? (
                <button type="button" className="email-link" disabled={submitting} onClick={() => setEmailMode('signin')}>
                  {t('login.alreadyHave')}
                </button>
              ) : (
                <button type="button" className="email-link" disabled={submitting} onClick={() => setEmailMode('register')}>
                  {t('login.needAccount')}
                </button>
              )}
            </p>
          </div>
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
        ) : !showOIDC && emailEnabled === false ? (
          <p className="login-unconfigured">{t('login.unconfigured')}</p>
        ) : null}
        {error && <p className="form-error">{error}</p>}
      </section>
    </main>
  );
}
