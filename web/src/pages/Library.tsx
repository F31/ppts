import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  getLibraryArtifacts,
  createDownload,
  type ClientIdentity,
  type LibraryArtifact
} from '../api';
import { useI18n } from '../i18n';
import { Link } from '../router';

const FORMAT_KEY: Record<string, string> = {
  mp4: 'artifacts.formatMp4',
  srt: 'artifacts.formatSrt',
  vtt: 'artifacts.formatVtt',
  web_project: 'artifacts.formatWebProject'
};

const FORMATS = ['mp4', 'srt', 'vtt', 'web_project'] as const;

const TIME_RANGES = [
  { key: 'all', labelKey: 'library.timeAll', days: 0 },
  { key: '7d', labelKey: 'library.time7d', days: 7 },
  { key: '30d', labelKey: 'library.time30d', days: 30 },
  { key: '90d', labelKey: 'library.time90d', days: 90 }
] as const;

function formatSize(bytes: number): string {
  const b = Number(bytes) || 0;
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`;
  return `${(b / 1024 / 1024).toFixed(1)} MB`;
}

// formatDuration 把毫秒渲染成「分:秒」（调用方保证 ms > 0；未知时长由调用方显示 t('common.none')）。
function formatDuration(ms: number): string {
  const total = Math.round(ms / 1000);
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

export function Library({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [artifacts, setArtifacts] = useState<LibraryArtifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // 筛选状态：格式 / 项目 / 时间范围。
  const [format, setFormat] = useState<string>('all');
  const [projectId, setProjectId] = useState<string>('all');
  const [range, setRange] = useState<string>('all');
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await getLibraryArtifacts(identity);
      setArtifacts(data.artifacts ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('library.loadFailed'));
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // 项目下拉：按 projectId 去重，展示 projectName（缺失时回退 id 前 8 位）。
  const projectOptions = useMemo(() => {
    const map = new Map<string, string>();
    for (const a of artifacts) {
      if (!map.has(a.projectId)) map.set(a.projectId, a.projectName || a.projectId.slice(0, 8));
    }
    return [...map.entries()].sort((x, y) => x[1].localeCompare(y[1]));
  }, [artifacts]);

  const now = Date.now();
  const rangeDays = TIME_RANGES.find((r) => r.key === range)?.days ?? 0;

  const filtered = useMemo(() => {
    return artifacts.filter((a) => {
      if (format !== 'all' && a.format !== format) return false;
      if (projectId !== 'all' && a.projectId !== projectId) return false;
      if (rangeDays > 0) {
        const created = new Date(a.createdAt).getTime();
        if (Number.isNaN(created) || now - created > rangeDays * 86400_000) return false;
      }
      return true;
    });
  }, [artifacts, format, projectId, rangeDays, now]);

  const onDownload = useCallback(
    async (a: LibraryArtifact) => {
      setDownloadingId(a.id);
      setDownloadError(null);
      try {
        const { signedUrl } = await createDownload(identity, a.id);
        const link = document.createElement('a');
        link.href = signedUrl;
        link.download = `${a.id}.${a.format === 'web_project' ? 'zip' : a.format}`;
        document.body.appendChild(link);
        link.click();
        link.remove();
      } catch (e) {
        setDownloadError(e instanceof Error ? e.message : t('library.downloadFailed'));
      } finally {
        setDownloadingId(null);
      }
    },
    [identity, t]
  );

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('library.eyebrow')}</span>
          <h1>{t('library.title')}</h1>
          <small className="page-sub">{t('library.subtitle')}</small>
        </div>
        <div className="page-actions">
          <Link to="/projects" className="button-ghost">{t('library.back')}</Link>
        </div>
      </section>

      {error && (
        <div className="load-failure" role="alert">
          <p className="form-error">{error}</p>
          <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
        </div>
      )}

      {loading ? (
        <p className="empty-state">{t('common.loading')}</p>
      ) : (
        <>
          <section className="panel library-filters">
            <span className="eyebrow">{t('library.filters')}</span>
            <div className="filter-row">
              <label className="filter-field">
                <span>{t('library.filterFormat')}</span>
                <select className="library-select" value={format} onChange={(e) => setFormat(e.currentTarget.value)}>
                  <option value="all">{t('library.allFormats')}</option>
                  {FORMATS.map((f) => (
                    <option key={f} value={f}>{t(FORMAT_KEY[f])}</option>
                  ))}
                </select>
              </label>
              <label className="filter-field">
                <span>{t('library.filterProject')}</span>
                <select className="library-select" value={projectId} onChange={(e) => setProjectId(e.currentTarget.value)}>
                  <option value="all">{t('library.allProjects')}</option>
                  {projectOptions.map(([id, name]) => (
                    <option key={id} value={id}>{name}</option>
                  ))}
                </select>
              </label>
              <label className="filter-field">
                <span>{t('library.filterTime')}</span>
                <select className="library-select" value={range} onChange={(e) => setRange(e.currentTarget.value)}>
                  {TIME_RANGES.map((r) => (
                    <option key={r.key} value={r.key}>{t(r.labelKey)}</option>
                  ))}
                </select>
              </label>
              <span className="library-count">{t('library.count', { count: filtered.length })}</span>
            </div>
          </section>

          <section className="panel">
            {filtered.length === 0 ? (
              <p className="empty-state">{t('library.noArtifacts')}</p>
            ) : (
              <ul className="library-grid">
                {filtered.map((a) => (
                  <li className="library-row" key={a.id}>
                    <span className="library-proj">
                      <Link to={`/projects/${a.projectId}/artifacts`} className="library-proj-link" title={a.projectName || a.projectId}>
                        {a.projectName || a.projectId.slice(0, 8)}
                      </Link>
                    </span>
                    <span className={`artifact-badge artifact-${a.format}`}>{t(FORMAT_KEY[a.format] ?? a.format)}</span>
                    <span className="artifact-meta">
                      {t('artifacts.duration')}: {a.durationMs > 0 ? formatDuration(a.durationMs) : t('common.none')}
                    </span>
                    <span className="artifact-meta">
                      {t('artifacts.size')}: {formatSize(a.sizeBytes)}
                    </span>
                    <span className="artifact-meta nowrap-ellipsis">{new Date(a.createdAt).toLocaleString()}</span>
                    <button
                      type="button"
                      className="button-ghost artifact-download"
                      disabled={!a.downloadable || downloadingId === a.id}
                      title={a.downloadable ? t('artifacts.downloadHint') : t('artifacts.downloadBlocked')}
                      onClick={() => void onDownload(a)}
                    >
                      {downloadingId === a.id ? t('artifacts.downloading') : t('artifacts.download')}
                    </button>
                    {/* A26：不可下载时必须说明原因，不留"点了没反应"的假按钮。 */}
                    {!a.downloadable && <span className="artifact-meta muted">{t('artifacts.downloadBlockedShort')}</span>}
                  </li>
                ))}
              </ul>
            )}
            {downloadError && <p className="form-error" role="alert">{downloadError}</p>}
          </section>
        </>
      )}
    </div>
  );
}
