import { useCallback, useEffect, useState } from 'react';
import { getPolicy, getQuota, getStorageUsage, getUsage, type ClientIdentity } from '../api';
import type { StorageUsage, TenantPolicy, TenantQuota, TenantUsage } from '../types';

function fmtBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

function fmtMinutes(seconds: number): string {
  return seconds <= 0 ? '0' : `${Math.round((seconds / 60) * 10) / 10}`;
}

export function SettingsUsage({ identity }: { identity: ClientIdentity }) {
  const [quota, setQuota] = useState<TenantQuota | null>(null);
  const [usage, setUsage] = useState<TenantUsage | null>(null);
  const [storage, setStorage] = useState<StorageUsage | null>(null);
  const [policy, setPolicy] = useState<TenantPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const month = new Date().toISOString().slice(0, 7);
      const [quotaRes, usageRes, storageRes, policyRes] = await Promise.all([
        getQuota(identity).catch(() => null),
        getUsage(identity, month).catch(() => null),
        getStorageUsage(identity).catch(() => null),
        getPolicy(identity).catch(() => null)
      ]);
      setQuota(quotaRes);
      setUsage(usageRes);
      setStorage(storageRes);
      setPolicy(policyRes);
    } catch (err) {
      setError(err instanceof Error ? err.message : '用量加载失败');
    } finally {
      setLoading(false);
    }
  }, [identity]);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return (
      <div className="page-stack">
        <p className="empty-state">加载中…</p>
      </div>
    );
  }

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">设置 · 用量与存储</span>
          <h1>用量与存储</h1>
          <small className="page-sub">数据来自 TenantService（配额/用量/存储/策略）。</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={() => void load()}>
            刷新
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}

      <section className="stat-grid" aria-label="配额与用量">
        <Stat label="月度生成额度" value={quota ? `${fmtMinutes(quota.monthlySeconds)} 分钟` : '—'} />
        <Stat label="本月已用" value={usage ? `${fmtMinutes(usage.secondsUsed)} 分钟` : '—'} />
        <Stat
          label="用量占比"
          value={quota && quota.monthlySeconds > 0 ? `${Math.round((Math.min(usage?.secondsUsed ?? 0, quota.monthlySeconds) / quota.monthlySeconds) * 100)}%` : '—'}
        />
        <Stat label="并发生成上限" value={quota ? String(quota.maxConcurrentJobs) : '—'} />
      </section>

      <section className="stat-grid" aria-label="成本分账">
        <Stat label="用户计费金额" value={usage ? `${usage.currency} ${usage.userAmount ?? 0}` : '—'} note="按定价表计算" />
        <Stat label="供应商成本" value={usage ? `${usage.currency} ${usage.supplierCost ?? 0}` : '—'} note="与用户计费分账" />
        <Stat label="账本数量" value={usage ? String(usage.costUnits) : '—'} note="月累计计量单位" />
      </section>

      <section className="panel">
        <header className="table-head">
          <h2>存储占用</h2>
        </header>
        {!storage ? (
          <p className="empty-state">存储用量暂不可用。</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>类别</th>
                <th>对象数</th>
                <th>字节数</th>
              </tr>
            </thead>
            <tbody>
              <Row label="源文件" objects={storage.sourceObjects} bytes={storage.sourceBytes} />
              <Row label="导出成品" objects={storage.artifactObjects} bytes={storage.artifactBytes} />
              <Row label="其他（音频/渲染/归档等）" objects={storage.otherObjects} bytes={storage.otherBytes} />
              <tr className="row-total">
                <td>合计</td>
                <td>{storage.sourceObjects + storage.artifactObjects + storage.otherObjects}</td>
                <td>{fmtBytes(storage.totalBytes)}</td>
              </tr>
            </tbody>
          </table>
        )}
      </section>

      <section className="panel">
        <header className="table-head">
          <h2>租户策略</h2>
        </header>
        {!policy ? (
          <p className="empty-state">租户策略暂不可用。</p>
        ) : (
          <dl className="detail-grid">
            <div>
              <dt>存储后端</dt>
              <dd>{policy.storageBackend || '(缺省 local)'}</dd>
            </div>
            <div>
              <dt>存储区域</dt>
              <dd>{policy.storageRegion || '(未设置)'}</dd>
            </div>
            <div>
              <dt>源文件保留期</dt>
              <dd>{policy.sourceRetentionDays > 0 ? `${policy.sourceRetentionDays} 天` : '(未设置)'}</dd>
            </div>
            <div>
              <dt>低频转换（天）</dt>
              <dd>{policy.storageTransitionDays > 0 ? `${policy.storageTransitionDays} 天` : '不下发'}</dd>
            </div>
            <div>
              <dt>过期（天）</dt>
              <dd>{policy.storageExpirationDays > 0 ? `${policy.storageExpirationDays} 天` : '不下发'}</dd>
            </div>
            <div>
              <dt>信封加密</dt>
              <dd>{policy.envelopeEncryption ? '已启用' : '未启用'}</dd>
            </div>
            <div>
              <dt>处理后删除源文件</dt>
              <dd>{policy.deleteSourceAfterDefault ? '默认删除' : '默认保留'}</dd>
            </div>
          </dl>
        )}
      </section>
    </div>
  );
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="stat-card">
      <span className="stat-label">{label}</span>
      <strong className="stat-value">{value}</strong>
      {note && <small className="stat-note">{note}</small>}
    </div>
  );
}

function Row({ label, objects, bytes }: { label: string; objects: number; bytes: number }) {
  return (
    <tr>
      <td>{label}</td>
      <td>{objects}</td>
      <td>{fmtBytes(bytes)}</td>
    </tr>
  );
}