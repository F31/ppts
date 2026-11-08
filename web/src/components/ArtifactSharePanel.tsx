import { useCallback, useEffect, useState } from 'react';
import { useI18n } from '../i18n';
import { can } from '../permissions';
import { describeApiError, settle } from '../apiError';
import {
  createArtifactShareLink,
  getArtifactShareStats,
  listArtifactShareLinks,
  revokeArtifactShareLink,
  shareUrl,
  type ClientIdentity,
  type LibraryArtifact
} from '../api';
import type { ArtifactShareLink, ArtifactShareStatsResponse, Role } from '../types';

// 作者侧成品分享面板（M6-3 前端接线 + V20 数据出口）。
//
// 这个面板存在的理由不只是「能复制链接」：后端从 2026-10-29 起就有完整的
// 成品分享端点，前端却一次都没调用过；V20 又把观看事件写进了审计表。
// 两件事凑在一起的后果是「有人在看、作者看不见」，所以这里把链接管理与
// 观看数据放在同一处，数据按链接按需加载。
//
// 四条诚实口径（artifact-share-console-check.mjs 静态钉住，缺一条判红）：
//   1. 只统计在线 /s/ 观看；离线包、隐私模式、DNT/GPC 不产生数据  → scopeNote
//   2. 数字只覆盖留存窗口，不是累计全量                          → retentionNote
//   3. 完播率分母写明是窗口内独立会话                            → denominatorNote
//   4. 采集总闸关闭 / 请求失败时显示说明，绝不渲染一排 0            → 下方两处早退
type StatsState = { loading: boolean; error: string; data?: ArtifactShareStatsResponse };

// 三个窗口写成显式字面量而不是 `days${d}`：i18n-check 只认源码里出现过的
// 完整 key，动态拼出来的键会变成「零引用词条」被判孤儿。

export function ArtifactSharePanel({
  identity, artifact, role, onClose
}: {
  identity: ClientIdentity;
  artifact: LibraryArtifact;
  role?: Role;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const canManage = can(role, 'project.share');
  const [links, setLinks] = useState<ArtifactShareLink[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [busy, setBusy] = useState(false);
  const [copiedId, setCopiedId] = useState('');
  const [expanded, setExpanded] = useState<string | null>(null);
  const [days, setDays] = useState<number>(30);
  const [statsByLink, setStatsByLink] = useState<Record<string, StatsState>>({});

  const [linkMode, setLinkMode] = useState('view_only');
  const [linkDays, setLinkDays] = useState(0);
  const [linkPassword, setLinkPassword] = useState('');

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    const res = await settle(() => listArtifactShareLinks(identity, artifact.projectId, artifact.id));
    if (res.error) {
      setLinks([]);
      setError(describeApiError(res.error, t('artifactShare.loadFailed'), t));
    } else {
      setLinks(res.data ?? []);
    }
    setLoading(false);
  }, [identity, artifact.projectId, artifact.id, t]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const loadStats = useCallback(async (linkId: string, windowDays: number) => {
    setStatsByLink((cur) => ({ ...cur, [linkId]: { loading: true, error: '' } }));
    const res = await settle(() => getArtifactShareStats(identity, artifact.projectId, artifact.id, linkId, windowDays));
    if (res.error) {
      // 失败态必须带原因且不带 data：渲染成 0 会让作者以为没人看。
      setStatsByLink((cur) => ({
        ...cur,
        [linkId]: { loading: false, error: describeApiError(res.error, t('artifactShare.statsFailed'), t) }
      }));
    } else {
      setStatsByLink((cur) => ({ ...cur, [linkId]: { loading: false, error: '', data: res.data ?? undefined } }));
    }
  }, [identity, artifact.projectId, artifact.id, t]);

  useEffect(() => {
    // 窗口一变就丢掉旧缓存：拿 30 天的数字配「近 7 天」的标签是另一种假数据。
    setStatsByLink({});
    if (expanded) void loadStats(expanded, days);
  }, [expanded, days, loadStats]);

  async function run(fn: () => Promise<unknown>, failKey: string) {
    setBusy(true);
    setActionError('');
    const res = await settle(fn);
    if (res.error) {
      setActionError(describeApiError(res.error, t(failKey), t));
    } else {
      await reload();
    }
    setBusy(false);
  }

  async function copy(link: ArtifactShareLink) {
    try {
      await navigator.clipboard.writeText(shareUrl(link.url));
      setCopiedId(link.id);
      window.setTimeout(() => setCopiedId(''), 2000);
    } catch {
      setActionError(t('artifactShare.copyFailed'));
    }
  }

  return (
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <div className="modal-card artifact-share-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <header>
          <div>
            <span className="eyebrow">{t('artifactShare.eyebrow')}</span>
            <h2>{t('artifactShare.title')}</h2>
            <p className="cell-sub">{artifact.projectName || artifact.projectId.slice(0, 8)} · {artifact.id.slice(0, 8)}</p>
          </div>
          <button type="button" onClick={onClose}>{t('common.close')}</button>
        </header>

        {/* honest-scope: 离线/隐私零上报、留存窗口、完播率分母——三条口径常驻，不随数据开关消失。 */}
        <div className="artifact-share-notes">
          <p>{t('artifactShare.scopeNote')}</p>
          <p>{t('artifactShare.retentionNote', { days: String(days) })}</p>
          <p>{t('artifactShare.denominatorNote')}</p>
        </div>

        {error ? <p className="form-error" role="alert">{error}</p> : null}
        {actionError ? <p className="form-error" role="alert">{actionError}</p> : null}
        {!canManage ? <p className="cell-sub">{t('share.readOnlyNote')}</p> : null}

        {canManage ? (
          <>
            <div className="artifact-share-toolbar">
              <label className="filter-field">
                <span>{t('artifactShare.days')}</span>
                <select
                  className="library-select"
                  value={days}
                  onChange={(e) => setDays(Number(e.currentTarget.value))}
                >
                  <option value={7}>{t('artifactShare.days7')}</option>
                  <option value={30}>{t('artifactShare.days30')}</option>
                  <option value={90}>{t('artifactShare.days90')}</option>
                </select>
              </label>
            </div>

            {loading ? <p className="cell-sub">{t('common.loading')}</p> : null}
            {!loading && links.length === 0 ? <p className="cell-sub">{t('artifactShare.noLinks')}</p> : null}
            <ul className="share-list artifact-share-list">
              {links.map((link) => (
                <li key={link.id}>
                  <div className="artifact-share-link-main">
                    <div className="share-who">
                      <strong>{link.revoked ? t('artifactShare.revoked') : (link.expiresAt ? t('artifactShare.expiryAt', { date: new Date(link.expiresAt).toLocaleDateString() }) : t('artifactShare.expiryNever'))}</strong>
                      <span className="member-subid">{link.passwordProtected ? t('artifactShare.passwordSet') : t('share.modeView')}</span>
                      <span className="cell-sub">{new Date(link.createdAt).toLocaleString()}</span>
                    </div>
                    <div className="share-row-actions">
                      <button type="button" disabled={link.revoked} onClick={() => void copy(link)}>
                        {copiedId === link.id ? t('artifactShare.copied') : t('artifactShare.copy')}
                      </button>
                      <button
                        type="button"
                        disabled={link.revoked || busy}
                        onClick={() => setExpanded((cur) => (cur === link.id ? null : link.id))}
                      >
                        {expanded === link.id ? t('artifactShare.hideStats') : t('artifactShare.showStats')}
                      </button>
                      {!link.revoked ? (
                        <button
                          type="button"
                          className="btn-danger"
                          disabled={busy}
                          onClick={() => void run(() => revokeArtifactShareLink(identity, artifact.projectId, artifact.id, link.id), 'artifactShare.revokeFailed')}
                        >
                          {t('artifactShare.revoke')}
                        </button>
                      ) : null}
                    </div>
                    {expanded === link.id ? <ShareStatsBlock state={statsByLink[link.id]} days={days} /> : null}
                  </div>
                </li>
              ))}
            </ul>

            <form
              className="share-form"
              onSubmit={(e) => {
                e.preventDefault();
                void run(
                  () => createArtifactShareLink(identity, artifact.projectId, artifact.id, {
                    accessMode: linkMode,
                    password: linkPassword || undefined,
                    expiresInDays: linkDays > 0 ? linkDays : undefined
                  }),
                  'artifactShare.createFailed'
                ).then(() => setLinkPassword(''));
              }}
            >
              <select value={linkMode} aria-label={t('share.mode')} onChange={(e) => setLinkMode(e.currentTarget.value)}>
                <option value="view_only">{t('share.modeView')}</option>
                <option value="view_and_comment">{t('share.modeComment')}</option>
              </select>
              <select value={linkDays} aria-label={t('share.expiry')} onChange={(e) => setLinkDays(Number(e.currentTarget.value))}>
                <option value={0}>{t('share.expiryNever')}</option>
                <option value={7}>{t('share.expiry7')}</option>
                <option value={30}>{t('share.expiry30')}</option>
              </select>
              <input
                type="password"
                value={linkPassword}
                placeholder={t('artifactShare.passwordOptional')}
                onChange={(e) => setLinkPassword(e.currentTarget.value)}
              />
              <button type="submit" className="btn btn-primary" disabled={busy}>{t('artifactShare.create')}</button>
            </form>
          </>
        ) : null}

        <div className="modal-actions">
          <button type="button" onClick={onClose}>{t('common.close')}</button>
        </div>
      </div>
    </div>
  );
}

// ShareStatsBlock 的四条早退是刻意的：加载中 / 失败 / 采集关闭 / 无数据都**不渲染数字区**。
// 只有 telemetryEnabled=true 且请求成功，才会走到下面那排指标。
function ShareStatsBlock({ state, days }: { state?: StatsState; days: number }) {
  const { t } = useI18n();
  if (!state || state.loading) {
    return <p className="cell-sub artifact-share-stats-note">{t('common.loading')}</p>;
  }
  if (state.error) {
    return <p className="form-error artifact-share-stats-note" role="alert">{state.error}</p>;
  }
  const data = state.data;
  if (!data) {
    return <p className="form-error artifact-share-stats-note" role="alert">{t('artifactShare.statsFailed')}</p>;
  }
  if (!data.telemetryEnabled) {
    return <p className="artifact-share-stats-note">{t('artifactShare.telemetryOff')}</p>;
  }
  const { stats } = data;
  const maxSessions = Math.max(1, stats.sessions, ...stats.progress.map((p) => p.sessions));
  return (
    <div className="artifact-share-stats">
      <p className="cell-sub">{data.retentionNote === 'audit_retention_window' ? t('artifactShare.retentionNote', { days: String(days) }) : ''}</p>
      <dl className="artifact-share-metrics">
        <div><dt>{t('artifactShare.views')}</dt><dd>{stats.views}</dd></div>
        <div><dt>{t('artifactShare.sessions')}</dt><dd>{stats.sessions}</dd></div>
        <div><dt>{t('artifactShare.completed')}</dt><dd>{stats.completed}</dd></div>
        <div><dt>{t('artifactShare.completionRate')}</dt><dd>{Math.round(stats.completionRate * 100)}%</dd></div>
      </dl>
      {stats.sessions === 0 ? (
        <p className="cell-sub">{t('artifactShare.noEvents')}</p>
      ) : (
        <div className="artifact-share-funnel">
          {stats.progress.map((p) => (
            <div className="artifact-share-funnel-row" key={p.bucket}>
              <span>{t('artifactShare.bucket', { bucket: String(p.bucket) })}</span>
              <div className="artifact-share-funnel-track">
                <div style={{ width: `${Math.round((p.sessions / maxSessions) * 100)}%` }} />
              </div>
              <strong>{p.sessions}</strong>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
