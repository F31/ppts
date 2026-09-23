import { useCallback, useEffect, useState } from 'react';
import { listAdminTenants, resumeTenant, suspendTenant, type AdminTenant, type ClientIdentity } from '../api';
import { describeApiError } from '../apiError';
import { useI18n } from '../i18n';

// Admin 是运营商后台：跨租户查看状态与配音额度，并挂起/恢复租户。
// 仅 identity.operator 为 true 时渲染（App 路由层已先判定；后端另有 403 兜底）。
export function Admin({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [tenants, setTenants] = useState<AdminTenant[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(async () => {
    setError('');
    try {
      setTenants(await listAdminTenants(identity));
    } catch (err) {
      setError(describeApiError(err, t('admin.loadFailed'), t));
    }
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const act = async (tn: AdminTenant) => {
    const suspending = tn.status !== 'suspended';
    const prompt = suspending ? t('admin.confirmSuspend', { name: tn.name }) : t('admin.resume');
    if (!window.confirm(prompt)) return;
    setBusy(tn.id);
    setError('');
    try {
      if (suspending) await suspendTenant(identity, tn.id);
      else await resumeTenant(identity, tn.id);
      await load();
    } catch (err) {
      setError(describeApiError(err, t('admin.failed'), t));
    } finally {
      setBusy('');
    }
  };

  return (
    <section className="page">
      <header className="page-header">
        <div>
          <span className="eyebrow">{t('admin.title')}</span>
          <h1>{t('admin.tenants')}</h1>
          <p>{t('admin.subtitle')}</p>
        </div>
      </header>
      {error && <p className="form-error" role="alert">{error}</p>}
      <table className="admin-table">
        <thead>
          <tr>
            <th>{t('admin.tenants')}</th>
            <th>{t('admin.type')}</th>
            <th>{t('admin.status')}</th>
            <th>{t('admin.usage')}</th>
            <th>{t('admin.createdAt')}</th>
            <th>{t('admin.actions')}</th>
          </tr>
        </thead>
        <tbody>
          {tenants.map((tn) => (
            <tr key={tn.id}>
              <td>
                {tn.name}
                <br />
                <small>{tn.id}</small>
              </td>
              <td>{tn.type}</td>
              <td>{tn.status === 'suspended' ? t('admin.suspended') : t('admin.active')}</td>
              <td>
                {tn.quota
                  ? tn.quota.unlimited
                    ? t('admin.unlimited')
                    : `${Math.round(tn.quota.consumed_units)} / ${Math.round(tn.quota.limit_units)}`
                  : '—'}
              </td>
              <td>{tn.created_at ? new Date(tn.created_at).toLocaleString() : '—'}</td>
              <td>
                <button type="button" disabled={busy === tn.id} onClick={() => void act(tn)}>
                  {tn.status === 'suspended' ? t('admin.resume') : t('admin.suspend')}
                </button>
              </td>
            </tr>
          ))}
          {tenants.length === 0 && (
            <tr>
              <td colSpan={6}>{t('admin.empty')}</td>
            </tr>
          )}
        </tbody>
      </table>
    </section>
  );
}
