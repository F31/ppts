import { useState } from 'react';
import type { ClientIdentity } from '../api';
import { oidcConfigured, isDevIdentityEnabled, startOIDCLogin } from '../auth';
import { useSession } from '../session';

const defaultTenantId = '00000000-0000-0000-0000-000000000000';

export function Login() {
  const { loginDev, loginOIDC } = useSession();
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
      setError('租户 ID 与用户 ID 不能为空');
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
      setError(err instanceof Error ? err.message : '企业账号登录失败');
      setSubmitting(false);
    }
  };

  const showOIDC = oidcConfigured();
  const showDev = isDevIdentityEnabled();

  return (
    <main className="login-page">
      <aside className="login-brand">
        <span className="brand-mark">智讲 PPT</span>
        <h1>让每一页 PPT，都有清晰的讲解</h1>
        <p>导入 PPT → 校对讲稿 → 试听调整 → 生成配音 → 播放与导出。</p>
      </aside>
      <section className="login-card">
        <span className="eyebrow">智讲 PPT 控制台</span>
        <h2>登录控制台</h2>
        {showOIDC && (
          <>
            <button type="button" className="primary-login" disabled={submitting} onClick={() => void onOIDC()}>
              {submitting ? '跳转中…' : '企业账号登录'}
            </button>
            <div className="login-divider">
              <span>或</span>
            </div>
          </>
        )}
        {showDev ? (
          <>
            <label>
              租户 ID
              <input value={tenantId} onChange={(e) => setTenantId(e.currentTarget.value)} placeholder={defaultTenantId} />
            </label>
            <label>
              用户 ID
              <input value={userId} onChange={(e) => setUserId(e.currentTarget.value)} placeholder="dev-user" />
            </label>
            <button type="button" className="dev-login" disabled={submitting} onClick={() => void submitDev()}>
              {submitting ? '登录中…' : '开发身份进入'}
            </button>
            <p className="dev-login-note">开发身份前后端需同时开启（API 需配置 PPTS_AUTH_DEV_HEADERS=true）。</p>
          </>
        ) : !showOIDC ? (
          <p className="login-unconfigured">登录服务尚未配置，请联系管理员。</p>
        ) : null}
        {error && <p className="form-error">{error}</p>}
      </section>
    </main>
  );
}