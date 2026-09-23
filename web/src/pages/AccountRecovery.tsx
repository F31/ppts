import { useEffect, useState } from 'react';
import { resetPassword, verifyEmail } from '../api';
import { describeApiError } from '../apiError';
import { useI18n } from '../i18n';
import { navigate } from '../router';

// AccountRecovery 同时承载两个无认证页面：
//   - mode="verify"：消费邮箱验证令牌（GET /verify-email?token=...）；
//   - mode="reset" ：用重置令牌设置新密码（GET /reset-password?token=...）。
export function AccountRecovery({ mode, token }: { mode: 'verify' | 'reset'; token: string }) {
  const { t } = useI18n();
  const [state, setState] = useState<'idle' | 'working' | 'done' | 'error'>(mode === 'verify' ? 'working' : 'idle');
  const [error, setError] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');

  useEffect(() => {
    if (mode !== 'verify') return;
    if (!token) {
      setState('error');
      setError(t('recovery.missingToken'));
      return;
    }
    let cancelled = false;
    verifyEmail(token)
      .then(() => {
        if (!cancelled) setState('done');
      })
      .catch((err) => {
        if (!cancelled) {
          setState('error');
          setError(describeApiError(err, t('recovery.verifyFailed'), t));
        }
      });
    return () => {
      cancelled = true;
    };
  }, [mode, token, t]);

  const submitReset = async () => {
    if (!token) {
      setError(t('recovery.missingToken'));
      return;
    }
    if (password !== confirm) {
      setError(t('recovery.passwordMismatch'));
      return;
    }
    setState('working');
    setError('');
    try {
      await resetPassword(token, password);
      setState('done');
    } catch (err) {
      setState('error');
      setError(describeApiError(err, t('recovery.resetFailed'), t));
    }
  };

  return (
    <main className="login-page">
      <aside className="login-brand">
        <span className="brand-mark">{t('shell.brand')}</span>
        <h1>{t('login.tagline')}</h1>
      </aside>
      <section className="login-card">
        <span className="eyebrow">{mode === 'verify' ? t('recovery.verifyEyebrow') : t('recovery.resetEyebrow')}</span>
        {state === 'done' ? (
          <>
            <p className="form-success" role="status">
              {mode === 'verify' ? t('recovery.verifyDone') : t('recovery.resetDone')}
            </p>
            <button type="button" className="primary-login" onClick={() => navigate('/login')}>
              {t('recovery.backToLogin')}
            </button>
          </>
        ) : (
          <>
            {mode === 'reset' && (
              <>
                <label>
                  {t('recovery.newPassword')}
                  <input
                    type="password"
                    value={password}
                    autoComplete="new-password"
                    onChange={(e) => setPassword(e.currentTarget.value)}
                  />
                </label>
                <label>
                  {t('recovery.confirmPassword')}
                  <input
                    type="password"
                    value={confirm}
                    autoComplete="new-password"
                    onChange={(e) => setConfirm(e.currentTarget.value)}
                  />
                </label>
                <button type="button" className="primary-login" disabled={state === 'working'} onClick={() => void submitReset()}>
                  {state === 'working' ? t('recovery.submitting') : t('recovery.resetSubmit')}
                </button>
              </>
            )}
            {mode === 'verify' && state === 'working' && <p className="login-unconfigured">{t('recovery.verifying')}</p>}
            {error && <p className="form-error" role="alert">{error}</p>}
            <p className="email-switch">
              <button type="button" className="email-link" onClick={() => navigate('/login')}>
                {t('recovery.backToLogin')}
              </button>
            </p>
          </>
        )}
      </section>
    </main>
  );
}
