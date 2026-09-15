import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  getNarration,
  getProjectSlides,
  getProjectArtifacts,
  createDownload,
  type ClientIdentity,
  type ProjectArtifact
} from '../api';
import { useI18n } from '../i18n';
import { Link } from '../router';

type SnapshotState = {
  loading: boolean;
  slideCount: number;
  ready: boolean;
  timelineKey: string;
  pagePngCount: number;
  revisionNo: number;
  unavailReason?: string;
};

const FORMAT_KEY: Record<string, string> = {
  mp4: 'artifacts.formatMp4',
  srt: 'artifacts.formatSrt',
  vtt: 'artifacts.formatVtt',
  web_project: 'artifacts.formatWebProject'
};

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

export function ProjectArtifacts({ identity, projectId }: { identity: ClientIdentity; projectId: string }) {
  const { t } = useI18n();
  const [snap, setSnap] = useState<SnapshotState>({
    loading: true,
    slideCount: 0,
    ready: false,
    timelineKey: '',
    pagePngCount: 0,
    revisionNo: 0
  });
  const [artifacts, setArtifacts] = useState<ProjectArtifact[]>([]);
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setSnap((c) => ({ ...c, loading: true }));
    try {
      let slideCount = 0;
      try {
        const slides = await getProjectSlides(identity, projectId);
        slideCount = slides.slides.length;
      } catch {
        // 解析未完成或不可用。
      }
      const narration = await getNarration(identity, projectId);
      setSnap({
        loading: false,
        slideCount,
        ready: narration.ready,
        timelineKey: narration.timelineKey,
        pagePngCount: narration.pagePngKeys?.length ?? 0,
        revisionNo: narration.revisionNo ?? 0
      });
      const list = await getProjectArtifacts(identity, projectId);
      setArtifacts(list.artifacts ?? []);
    } catch (err) {
      setSnap((c) => ({
        ...c,
        loading: false,
        unavailReason: err instanceof Error ? err.message : t('artifacts.loadFailed')
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, projectId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const groups = useMemo(() => {
    const m = new Map<string, ProjectArtifact[]>();
    for (const a of artifacts) {
      const arr = m.get(a.snapshotHash) ?? [];
      arr.push(a);
      m.set(a.snapshotHash, arr);
    }
    return [...m.entries()];
  }, [artifacts]);

  const onDownload = useCallback(
    async (a: ProjectArtifact) => {
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
        setDownloadError(e instanceof Error ? e.message : t('artifacts.downloadFailed'));
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
          <span className="eyebrow">{t('artifacts.eyebrow')}</span>
          <h1 title={projectId}>{projectId.slice(0, 14)}</h1>
          <small className="page-sub">{t('artifacts.subtitle')}</small>
        </div>
        <div className="page-actions">
          <Link to={`/projects/${projectId}/editor`} className="button-primary">
            {t('artifacts.back')}
          </Link>
          <Link to="/jobs" className="button-ghost">
            {t('artifacts.jobs')}
          </Link>
        </div>
      </section>

      {snap.loading ? (
        <p className="empty-state">{t('common.loading')}</p>
      ) : (
        <>
          <section className="panel">
            <span className="eyebrow">{t('artifacts.snapshot')}</span>
            {snap.ready ? (
              <dl className="detail-grid">
                <div>
                  <dt>{t('artifacts.status')}</dt>
                  <dd>
                    <span className="state-tag succeeded">{t('artifacts.voiced')}</span>
                  </dd>
                </div>
                <div>
                  <dt>{t('artifacts.pageCount')}</dt>
                  <dd>{snap.slideCount}</dd>
                </div>
                <div>
                  <dt>{t('artifacts.pageRenders')}</dt>
                  <dd>
                    {snap.pagePngCount > 0
                      ? t('artifacts.pageCountValue', { count: snap.pagePngCount })
                      : t('artifacts.notRendered')}
                  </dd>
                </div>
                <div>
                  <dt>{t('artifacts.sourceRevision')}</dt>
                  <dd>rev {snap.revisionNo || t('artifacts.unknown')}</dd>
                </div>
                <div>
                  <dt>{t('artifacts.timeline')}</dt>
                  <dd className="nowrap-ellipsis">{snap.timelineKey}</dd>
                </div>
              </dl>
            ) : (
              <div className="empty-state first-run">
                <p>
                  {snap.unavailReason
                    ? t('artifacts.unavailable', { reason: snap.unavailReason })
                    : t('artifacts.none')}
                </p>
                <Link to={`/projects/${projectId}/editor`} className="button-primary">
                  {t('artifacts.goGenerate')}
                </Link>
              </div>
            )}
          </section>

          <section className="panel">
            <span className="eyebrow">{t('artifacts.exports')}</span>
            <p className="panel-note">{t('artifacts.exportsNote')}</p>
            {artifacts.length === 0 ? (
              <p className="empty-state">{t('artifacts.noArtifacts')}</p>
            ) : (
              <div className="artifact-groups">
                {groups.map(([hash, items]) => (
                  <div className="artifact-group" key={hash}>
                    <h3 className="artifact-group-title">
                      {t('artifacts.snapshotGroup', { hash: hash.slice(0, 8) })}
                    </h3>
                    <ul className="artifact-list">
                      {items.map((a) => (
                        <li className="artifact-row" key={a.id}>
                          <span className={`artifact-badge artifact-${a.format}`}>
                            {t(FORMAT_KEY[a.format] ?? a.format)}
                          </span>
                          <span className="artifact-meta">
                            {t('artifacts.createdAt')}: {new Date(a.createdAt).toLocaleString()}
                          </span>
                          <span className="artifact-meta">
                            {t('artifacts.size')}: {formatSize(a.sizeBytes)}
                          </span>
                          <button
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
                  </div>
                ))}
              </div>
            )}
            {downloadError && <p className="form-error">{downloadError}</p>}
          </section>
        </>
      )}
    </div>
  );
}
