import { useCallback, useEffect, useState } from 'react';
import { getPolicy, getQuota, getStorageUsage, getUsage, type ClientIdentity } from '../api';
import { describeApiError, settle } from '../apiError';
import { useI18n } from '../i18n';
import type { StorageUsage, TenantPolicy, TenantQuota, TenantUsage } from '../types';

// 后端走 Protobuf-JSON：int64 字段编码为字符串，且零值字段会被省略（undefined）。
// 这里统一按数字解析，缺失/非法值按 0 处理，避免 undefined.toFixed 抛错导致整页白屏。
function fmtBytes(bytes: number): string {
  const b = Number(bytes) || 0;
  if (b <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = b;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

function fmtMinutes(seconds: number): string {
  const s = Number(seconds) || 0;
  return s <= 0 ? '0' : `${Math.round((s / 60) * 10) / 10}`;
}

export function SettingsUsage({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [quota, setQuota] = useState<TenantQuota | null>(null);
  const [usage, setUsage] = useState<TenantUsage | null>(null);
  const [storage, setStorage] = useState<StorageUsage | null>(null);
  const [policy, setPolicy] = useState<TenantPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  // A26：分区错误单独记录（核心 / 存储 / 策略），失败时给出原因与重试，不再统一渲染成"暂不可用"。
  // 核心区 = 配额 + 用量，是这一页唯一会被用户直接拿去质疑计费口径的数字，必须能解释。
  const [coreError, setCoreError] = useState('');
  const [storageError, setStorageError] = useState('');
  const [policyError, setPolicyError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setCoreError('');
    setStorageError('');
    setPolicyError('');
    const month = new Date().toISOString().slice(0, 7);
    const [quotaRes, usageRes, storageRes, policyRes] = await Promise.all([
      settle(() => getQuota(identity)),
      settle(() => getUsage(identity, month)),
      settle(() => getStorageUsage(identity)),
      settle(() => getPolicy(identity))
    ]);
    setQuota(quotaRes.data);
    setUsage(usageRes.data);
    setStorage(storageRes.data);
    setPolicy(policyRes.data);
    if (quotaRes.error || usageRes.error) {
      setCoreError(describeApiError(quotaRes.error ?? usageRes.error, t('usage.loadFailed'), t));
    }
    if (storageRes.error) {
      setStorageError(describeApiError(storageRes.error, t('usage.storageFailed'), t));
    }
    if (policyRes.error) {
      setPolicyError(describeApiError(policyRes.error, t('usage.policyFailed'), t));
    }
    setLoading(false);
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

      {/* A26：核心区失败必须给原因 + 重试。此前 coreError 只被赋值、从未渲染，
          配额/用量拉不到时指标卡只剩一排「—」，用户只能理解为"平台没这个数"。 */}
      {coreError && (
        <div className="load-failure" role="alert">
          <p className="form-error">{coreError}</p>
          <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
        </div>
      )}

      <section className="stat-grid" aria-label={t('usage.title')}>
        <Stat label={t('usage.monthlyQuota')} value={quota ? t('usage.minutes', { n: fmtMinutes(quota.monthlySeconds) }) : '—'} />
        <Stat label={t('usage.used')} value={usage ? t('usage.minutes', { n: fmtMinutes(usage.secondsUsed) }) : '—'} />
        <Stat
          label={t('usage.ratio')}
          value={quota && quota.monthlySeconds > 0 ? `${Math.round((Math.min(usage?.secondsUsed ?? 0, quota.monthlySeconds) / quota.monthlySeconds) * 100)}%` : '—'}
        />
        <Stat label={t('usage.maxConcurrent')} value={quota ? String(quota.maxConcurrentJobs ?? 0) : '—'} />
      </section>

      <section className="stat-grid" aria-label={t('usage.supplierCost')}>
        <Stat label={t('usage.userAmount')} value={usage ? `${usage.currency} ${usage.userAmount ?? 0}` : '—'} note={t('usage.byPricing')} />
        <Stat label={t('usage.supplierCost')} value={usage ? `${usage.currency} ${usage.supplierCost ?? 0}` : '—'} note={t('usage.splitBilling')} />
        <Stat label={t('usage.costUnits')} value={usage ? String(usage.costUnits ?? 0) : '—'} note={t('usage.monthlyUnits')} />
      </section>

      <section className="panel">
        <header className="table-head">
          <h2>{t('usage.storageTitle')}</h2>
        </header>
        {storageError ? (
          <div className="load-failure" role="alert">
            <p className="form-error">{storageError}</p>
            <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
          </div>
        ) : !storage ? (
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
                <td>{(Number(storage.sourceObjects) || 0) + (Number(storage.artifactObjects) || 0) + (Number(storage.otherObjects) || 0)}</td>
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
        {policyError ? (
          <div className="load-failure" role="alert">
            <p className="form-error">{policyError}</p>
            <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
          </div>
        ) : !policy ? (
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
      <td>{Number(objects) || 0}</td>
      <td>{fmtBytes(bytes)}</td>
    </tr>
  );
}