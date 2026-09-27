import { useCallback, useEffect, useState } from 'react';
import { listAdminTenants, resumeTenant, suspendTenant, type AdminTenant, type ClientIdentity } from '../api';
import { describeApiError } from '../apiError';
import { useI18n } from '../i18n';

const PAGE_SIZES = [10, 20, 30, 50, 100];

// Admin 是运营商后台：跨租户查看状态与配音额度，并挂起/恢复租户。
// 仅 identity.operator 为 true 时渲染（App 路由层已先判定；后端另有 403 兜底）。
// 列表使用游标分页（keyset）：前进记录历史游标以支持后退，不重复拉取。
export function Admin({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [tenants, setTenants] = useState<AdminTenant[]>([]);
  const [total, setTotal] = useState(0);
  const [cursor, setCursor] = useState('');
  const [nextCursor, setNextCursor] = useState('');
  const [history, setHistory] = useState<string[]>([]);
  const [pageSize, setPageSize] = useState(20);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(
    async (cursorArg?: string, size?: number) => {
      setLoading(true);
      setError('');
      try {
        const page = await listAdminTenants(identity, {
          cursor: cursorArg || undefined,
          pageSize: size ?? pageSize
        });
        setTenants(page.tenants);
        setNextCursor(page.nextCursor);
        setTotal(page.total);
        setCursor(cursorArg ?? '');
      } catch (err) {
        setError(describeApiError(err, t('admin.loadFailed'), t));
      } finally {
        setLoading(false);
      }
    },
    [identity, pageSize, t]
  );

  useEffect(() => {
    void load();
  }, [load]);

  const goNext = () => {
    if (!nextCursor || loading) return;
    setHistory((prev) => [...prev, cursor]);
    void load(nextCursor);
  };

  const goPrev = () => {
    if (history.length === 0 || loading) return;
    const prev = history[history.length - 1];
    setHistory((prevHistory) => prevHistory.slice(0, -1));
    void load(prev);
  };

  const onChangePageSize = (size: number) => {
    setPageSize(size);
    setHistory([]);
    void load('', size);
  };

  const act = async (tn: AdminTenant) => {
    const suspending = tn.status !== 'suspended';
    const prompt = suspending ? t('admin.confirmSuspend', { name: tn.name }) : t('admin.resume');
    if (!window.confirm(prompt)) return;
    setBusy(tn.id);
    setError('');
    try {
      if (suspending) await suspendTenant(identity, tn.id);
      else await resumeTenant(identity, tn.id);
      await load(cursor);
    } catch (err) {
      setError(describeApiError(err, t('admin.failed'), t));
    } finally {
      setBusy('');
    }
  };

  const pageIndex = history.length;

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

      <section className="panel">
        <header className="table-head">
          <h2>{t('admin.tenants')}</h2>
          <div className="pagination">
            <label className="page-size">
              {t('admin.pageSize')}
              <select value={pageSize} onChange={(e) => onChangePageSize(Number(e.target.value))}>
                {PAGE_SIZES.map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </select>
            </label>
            <button type="button" disabled={pageIndex === 0 || loading} onClick={goPrev}>
              {t('admin.prevPage')}
            </button>
            <span className="page-indicator">
              {total > 0 ? `${tenants.length} / ${total}` : t('admin.pageOf', { page: pageIndex + 1 })}
            </span>
            <button type="button" disabled={!nextCursor || loading} onClick={goNext}>
              {t('admin.nextPage')}
            </button>
          </div>
        </header>

        {loading ? (
          <p className="panel-note">{t('admin.loading')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('admin.tenants')}</th>
                <th>{t('admin.type')}</th>
                <th>{t('admin.status')}</th>
                <th>{t('admin.usage')}</th>
                <th>{t('admin.createdAt')}</th>
                <th className="col-actions">{t('admin.actions')}</th>
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
                  <td className="col-actions">
                    <div className="row-actions">
                      <button type="button" disabled={busy === tn.id} onClick={() => void act(tn)}>
                        {tn.status === 'suspended' ? t('admin.resume') : t('admin.suspend')}
                      </button>
                    </div>
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
        )}
      </section>
    </section>
  );
}
