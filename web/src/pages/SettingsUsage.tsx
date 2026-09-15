import { useCallback, useEffect, useState } from 'react';
import { getPolicy, getQuota, getStorageUsage, getUsage, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
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
  const { t } = useI18n();
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
      setError(err instanceof Error ? err.message : t('usage.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return (
      <div className="page-stack">
        <p className="empty-state">{t('common.loading')}</p>
      </div>
    );
  }

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('usage.eyebrow')}</span>
          <h1>{t('usage.title')}</h1>
          <small className="page-sub">{t('usage.subtitle')}</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}

      <section className="stat-grid" aria-label={t('usage.title')}>
        <Stat label={t('usage.monthlyQuota')} value={quota ? t('usage.minutes', { n: fmtMinutes(quota.monthlySeconds) }) : '—'} />
        <Stat label={t('usage.used')} value={usage ? t('usage.minutes', { n: fmtMinutes(usage.secondsUsed) }) : '—'} />
        <Stat
          label={t('usage.ratio')}
          value={quota && quota.monthlySeconds > 0 ? `${Math.round((Math.min(usage?.secondsUsed ?? 0, quota.monthlySeconds) / quota.monthlySeconds) * 100)}%` : '—'}
        />
        <Stat label={t('usage.maxConcurrent')} value={quota ? String(quota.maxConcurrentJobs) : '—'} />
      </section>

      <section className="stat-grid" aria-label={t('usage.supplierCost')}>
        <Stat label={t('usage.userAmount')} value={usage ? `${usage.currency} ${usage.userAmount ?? 0}` : '—'} note={t('usage.byPricing')} />
        <Stat label={t('usage.supplierCost')} value={usage ? `${usage.currency} ${usage.supplierCost ?? 0}` : '—'} note={t('usage.splitBilling')} />
        <Stat label={t('usage.costUnits')} value={usage ? String(usage.costUnits) : '—'} note={t('usage.monthlyUnits')} />
      </section>

      <section className="panel">
        <header className="table-head">
          <h2>{t('usage.storageTitle')}</h2>
        </header>
        {!storage ? (
          <p className="empty-state">{t('usage.storageUnavailable')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('usage.colCategory')}</th>
                <th>{t('usage.colObjects')}</th>
                <th>{t('usage.colBytes')}</th>
              </tr>
            </thead>
            <tbody>
              <Row label={t('usage.catSource')} objects={storage.sourceObjects} bytes={storage.sourceBytes} />
              <Row label={t('usage.catArtifact')} objects={storage.artifactObjects} bytes={storage.artifactBytes} />
              <Row label={t('usage.catOther')} objects={storage.otherObjects} bytes={storage.otherBytes} />
              <tr className="row-total">
                <td>{t('usage.total')}</td>
                <td>{storage.sourceObjects + storage.artifactObjects + storage.otherObjects}</td>
                <td>{fmtBytes(storage.totalBytes)}</td>
              </tr>
            </tbody>
          </table>
        )}
      </section>

      <section className="panel">
        <header className="table-head">
          <h2>{t('usage.policyTitle')}</h2>
        </header>
        {!policy ? (
          <p className="empty-state">{t('usage.policyUnavailable')}</p>
        ) : (
          <dl className="detail-grid">
            <div>
              <dt>{t('usage.backend')}</dt>
              <dd>{policy.storageBackend || t('usage.defaultBackend')}</dd>
            </div>
            <div>
              <dt>{t('usage.region')}</dt>
              <dd>{policy.storageRegion || t('usage.notSet')}</dd>
            </div>
            <div>
              <dt>{t('usage.retention')}</dt>
              <dd>{policy.sourceRetentionDays > 0 ? t('usage.days', { n: policy.sourceRetentionDays }) : t('usage.notSet')}</dd>
            </div>
            <div>
              <dt>{t('usage.transition')}</dt>
              <dd>{policy.storageTransitionDays > 0 ? t('usage.days', { n: policy.storageTransitionDays }) : t('usage.notIssued')}</dd>
            </div>
            <div>
              <dt>{t('usage.expiration')}</dt>
              <dd>{policy.storageExpirationDays > 0 ? t('usage.days', { n: policy.storageExpirationDays }) : t('usage.notIssued')}</dd>
            </div>
            <div>
              <dt>{t('usage.envelope')}</dt>
              <dd>{policy.envelopeEncryption ? t('usage.enabled') : t('usage.disabled')}</dd>
            </div>
            <div>
              <dt>{t('usage.deleteSource')}</dt>
              <dd>{policy.deleteSourceAfterDefault ? t('usage.defaultDelete') : t('usage.defaultKeep')}</dd>
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