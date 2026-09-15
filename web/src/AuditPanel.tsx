import { useCallback, useEffect, useState } from 'react';
import { ConnectError, listAuditArchives, listAuditEvents, type ClientIdentity } from './api';
import { useI18n } from './i18n';
import type { AuditArchiveFile, AuditEvent } from './types';

type Props = {
  identity: ClientIdentity;
};

type AuditState = {
  loading: boolean;
  error: string;
  events: AuditEvent[];
  archives: AuditArchiveFile[];
};

const emptyState: AuditState = { loading: false, error: '', events: [], archives: [] };

function formatTime(unix: number, locale: string) {
  if (!unix) return '-';
  return new Date(unix * 1000).toLocaleString(locale, { hour12: false });
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

export function AuditPanel({ identity }: Props) {
  const { t, lang } = useI18n();
  const locale = lang === 'zh' ? 'zh-CN' : 'en-US';
  const [action, setAction] = useState('');
  const [resourceType, setResourceType] = useState('');
  const [sinceHours, setSinceHours] = useState('24');
  const [state, setState] = useState<AuditState>(emptyState);

  const load = useCallback(async () => {
    setState((current) => ({ ...current, loading: true, error: '' }));
    try {
      const hours = Number.parseInt(sinceHours, 10);
      const sinceUnix = Number.isFinite(hours) && hours > 0 ? Math.floor(Date.now() / 1000) - hours * 3600 : 0;
      const [events, archives] = await Promise.all([
        listAuditEvents(identity, { action, resourceType, sinceUnix, pageSize: 50 }),
        listAuditArchives(identity, 20)
      ]);
      setState({ loading: false, error: '', events, archives });
    } catch (error) {
      const denied = error instanceof ConnectError && error.code === 'permission_denied';
      setState({
        loading: false,
        error: denied ? t('audit.denied') : error instanceof Error ? error.message : t('audit.loadFailed'),
        events: [],
        archives: []
      });
    }
  }, [identity, action, resourceType, sinceHours, t]);

  // 筛选条件变化即自动重查（文本输入防抖 300ms）。
  // A26：此前筛选只改本地 state、必须另点"刷新"才生效，观感上像"筛选已应用"的假交互。
  useEffect(() => {
    const timer = window.setTimeout(() => {
      void load();
    }, 300);
    return () => window.clearTimeout(timer);
  }, [load]);

  return (
    <section className="audit-panel" aria-label={t('audit.aria')}>
      <header>
        <div>
          <span className="eyebrow">{t('audit.eyebrow')}</span>
          <h2>{t('audit.title')}</h2>
        </div>
        <button type="button" disabled={state.loading} onClick={() => void load()}>
          {state.loading ? t('audit.refreshing') : t('common.refresh')}
        </button>
      </header>
      <div className="audit-filters">
        <input value={action} onChange={(event) => setAction(event.target.value)} placeholder={t('audit.filterAction')} />
        <input value={resourceType} onChange={(event) => setResourceType(event.target.value)} placeholder={t('audit.filterResource')} />
        <select value={sinceHours} onChange={(event) => setSinceHours(event.target.value)}>
          <option value="1">{t('audit.last1h')}</option>
          <option value="24">{t('audit.last24h')}</option>
          <option value="168">{t('audit.last7d')}</option>
          <option value="0">{t('audit.all')}</option>
        </select>
      </div>
      {state.error && <p className="api-status error">{state.error}</p>}
      <div className="audit-list">
        {state.events.length === 0 && !state.error ? (
          <p className="empty-state">{t('audit.empty')}</p>
        ) : (
          state.events.map((event) => (
            <article key={event.id}>
              <div>
                <strong>{event.action}</strong>
                <span>{formatTime(event.createdAtUnix, locale)}</span>
              </div>
              <p>{event.resourceType} · {event.resourceId || '-'}</p>
              <small>{event.actorUser || 'system'}</small>
            </article>
          ))
        )}
      </div>
      <div className="archive-list">
        <strong>{t('audit.archives')}</strong>
        {state.archives.length === 0 ? (
          <p>{t('audit.noArchives')}</p>
        ) : (
          state.archives.map((file) => (
            <div key={file.objectKey}>
              <span>{file.objectKey}</span>
              <small>{formatSize(file.sizeBytes)} · {formatTime(file.updatedAtUnix, locale)}</small>
            </div>
          ))
        )}
      </div>
    </section>
  );
}
