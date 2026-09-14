import { useEffect, useState } from 'react';
import { ConnectError, listAuditArchives, listAuditEvents, type ClientIdentity } from './api';
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

function formatTime(unix: number) {
  if (!unix) return '-';
  return new Date(unix * 1000).toLocaleString('zh-CN', { hour12: false });
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

export function AuditPanel({ identity }: Props) {
  const [action, setAction] = useState('');
  const [resourceType, setResourceType] = useState('');
  const [sinceHours, setSinceHours] = useState('24');
  const [state, setState] = useState<AuditState>(emptyState);

  const load = async () => {
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
        error: denied ? '当前用户需要 admin 或 owner 角色才能查看审计。' : error instanceof Error ? error.message : '审计加载失败',
        events: [],
        archives: []
      });
    }
  };

  useEffect(() => {
    void load();
  }, []);

  return (
    <section className="audit-panel" aria-label="租户审计">
      <header>
        <div>
          <span className="eyebrow">Tenant Audit</span>
          <h2>审计与归档</h2>
        </div>
        <button type="button" disabled={state.loading} onClick={() => void load()}>
          {state.loading ? '刷新中…' : '刷新'}
        </button>
      </header>
      <div className="audit-filters">
        <input value={action} onChange={(event) => setAction(event.target.value)} placeholder="action 过滤" />
        <input value={resourceType} onChange={(event) => setResourceType(event.target.value)} placeholder="resource_type" />
        <select value={sinceHours} onChange={(event) => setSinceHours(event.target.value)}>
          <option value="1">近 1 小时</option>
          <option value="24">近 24 小时</option>
          <option value="168">近 7 天</option>
          <option value="0">全部</option>
        </select>
      </div>
      {state.error && <p className="api-status error">{state.error}</p>}
      <div className="audit-list">
        {state.events.length === 0 && !state.error ? (
          <p className="empty-state">暂无匹配审计事件。</p>
        ) : (
          state.events.map((event) => (
            <article key={event.id}>
              <div>
                <strong>{event.action}</strong>
                <span>{formatTime(event.createdAtUnix)}</span>
              </div>
              <p>{event.resourceType} · {event.resourceId || '-'}</p>
              <small>{event.actorUser || 'system'}</small>
            </article>
          ))
        )}
      </div>
      <div className="archive-list">
        <strong>归档文件</strong>
        {state.archives.length === 0 ? (
          <p>暂无归档。</p>
        ) : (
          state.archives.map((file) => (
            <div key={file.objectKey}>
              <span>{file.objectKey}</span>
              <small>{formatSize(file.sizeBytes)} · {formatTime(file.updatedAtUnix)}</small>
            </div>
          ))
        )}
      </div>
    </section>
  );
}
