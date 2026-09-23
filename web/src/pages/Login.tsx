import { useEffect, useState } from 'react';
import type { ClientIdentity } from '../api';
import { oidcConfigured, isDevIdentityEnabled, startOIDCLogin } from '../auth';
import { getAuthConfig, loginEmail, registerEmail, forgotPassword } from '../api';
import { useI18n } from '../i18n';
import { useSession } from '../session';
import { describeApiError } from '../apiError';

const defaultTenantId = '00000000-0000-0000-0000-000000000000';
type EmailMode = 'signin' | 'register' | 'forgot';
type AccountType = 'personal' | 'organization';

// 组织名称校验规则（与后端 internal/api/emailauth.go 的 validateOrgName 一致）：
// 长度 2~40 个字符，允许中英文/数字/空格/-_&.。返回 i18n 键，合法返回 null。
const ORG_NAME_MIN = 2;
const ORG_NAME_MAX = 40;

function orgNameError(name: string): string | null {
  const chars = Array.from(name); // 按码点计数，对齐 Go 的 utf8.RuneCountInString
  if (chars.length < ORG_NAME_MIN || chars.length > ORG_NAME_MAX) return 'login.orgNameLength';
  for (const ch of chars) {
    if (/[\p{L}\p{N}]/u.test(ch) || ' -_&.'.includes(ch)) continue;
    return 'login.orgNameInvalid';
  }
  return null;
}

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
  // 注册的账号类型：个人（默认，单成员租户）或组织（需填组织名称）。
  const [accountType, setAccountType] = useState<AccountType>('personal');
  const [orgName, setOrgName] = useState('');
  // 注册时采集的可选档案字段（成员列表富字段展示）。
  const [regUsername, setRegUsername] = useState('');
  const [regFullName, setRegFullName] = useState('');
  const [regGender, setRegGender] = useState('');
  const [regBirthDate, setRegBirthDate] = useState('');
  const [regPhone, setRegPhone] = useState('');
  // forgotSent：忘记密码提交后的统一提示（不泄露账号是否存在）。
  const [forgotSent, setForgotSent] = useState(false);

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
    if (!trimmedEmail) {
      setError(t('login.emailRequired'));
      setSubmitting(false);
      return;
    }
    if (emailMode === 'forgot') {
      try {
        await forgotPassword(trimmedEmail);
        setForgotSent(true);
      } catch (err) {
        setError(describeApiError(err, t('login.forgotFailed'), t));
      } finally {
        setSubmitting(false);
      }
      return;
    }
    if (!password) {
      setError(t('login.emailRequired'));
      setSubmitting(false);
      return;
    }
    const trimmedOrg = orgName.trim();
    if (emailMode === 'register' && accountType === 'organization') {
      const orgErr = orgNameError(trimmedOrg);
      if (orgErr) {
        setError(t(orgErr));
        setSubmitting(false);
        return;
      }
    }
    try {
      const res =
        emailMode === 'register'
          ? await registerEmail({
              account: trimmedEmail,
              password,
              accountType,
              orgName: accountType === 'organization' ? trimmedOrg : undefined,
              username: regUsername,
              fullName: regFullName,
              gender: regGender,
              birthDate: regBirthDate,
              phone: regPhone,
            })
          : await loginEmail({ account: trimmedEmail, password });
      loginEmailSession({
        tenantId: res.tenant_id,
        userId: res.user_id,
        cookieSession: true,
        account: res.account,
        accountKind: res.account_kind,
        emailVerified: res.email_verified,
        tenantName: res.tenant_name,
        tenantType: res.tenant_type,
      });
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
  // 组织名称的行内提示与提交门禁：仅在"组织"模式下校验。
  const registerOrg = emailMode === 'register' && accountType === 'organization';
  const orgErr = registerOrg && orgName.trim() !== '' ? orgNameError(orgName.trim()) : null;
  const registerBlocked = registerOrg && orgNameError(orgName.trim()) !== null;

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
            {emailMode !== 'forgot' && (
              <label>
                {t('login.password')}
                <input
                  type="password"
                  value={password}
                  autoComplete={emailMode === 'register' ? 'new-password' : 'current-password'}
                  onChange={(e) => setPassword(e.currentTarget.value)}
                />
              </label>
            )}
            {emailMode === 'register' && (
              <fieldset className="account-type">
                <legend>{t('login.accountType')}</legend>
                <label className={`account-type-option ${accountType === 'personal' ? 'selected' : ''}`}>
                  <input
                    type="radio"
                    name="accountType"
                    checked={accountType === 'personal'}
                    onChange={() => setAccountType('personal')}
                  />
                  <span>
                    <strong>{t('login.accountPersonal')}</strong>
                    <small>{t('login.accountPersonalHint')}</small>
                  </span>
                </label>
                <label className={`account-type-option ${accountType === 'organization' ? 'selected' : ''}`}>
                  <input
                    type="radio"
                    name="accountType"
                    checked={accountType === 'organization'}
                    onChange={() => setAccountType('organization')}
                  />
                  <span>
                    <strong>{t('login.accountOrganization')}</strong>
                    <small>{t('login.accountOrganizationHint')}</small>
                  </span>
                </label>
                {accountType === 'organization' && (
                  <label className="org-name">
                    {t('login.orgName')}
                    <input
                      value={orgName}
                      placeholder={t('login.orgNamePlaceholder')}
                      onChange={(e) => setOrgName(e.currentTarget.value)}
                    />
                    {orgErr && <small className="field-error" role="alert">{t(orgErr)}</small>}
                  </label>
                )}
              </fieldset>
            )}
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
            {forgotSent ? (
              <p className="form-success" role="status">{t('login.forgotSent')}</p>
            ) : (
              <button type="button" className="primary-login" disabled={submitting || registerBlocked} onClick={() => void submitEmail()}>
                {submitting
                  ? t('login.submitting')
                  : emailMode === 'register'
                    ? t('login.register')
                    : emailMode === 'forgot'
                      ? t('login.forgotSubmit')
                      : t('login.signIn')}
              </button>
            )}
            <p className="email-switch">
              {emailMode === 'register' ? (
                <button type="button" className="email-link" disabled={submitting} onClick={() => setEmailMode('signin')}>
                  {t('login.alreadyHave')}
                </button>
              ) : emailMode === 'forgot' ? (
                <button type="button" className="email-link" disabled={submitting} onClick={() => { setEmailMode('signin'); setForgotSent(false); }}>
                  {t('login.backToSignIn')}
                </button>
              ) : (
                <>
                  <button type="button" className="email-link" disabled={submitting} onClick={() => setEmailMode('register')}>
                    {t('login.needAccount')}
                  </button>
                  <button type="button" className="email-link" disabled={submitting} onClick={() => setEmailMode('forgot')}>
                    {t('login.forgotPassword')}
                  </button>
                </>
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
        {error && <p className="form-error" role="alert">{error}</p>}
      </section>
    </main>
  );
}
