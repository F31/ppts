import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  getLibraryArtifacts,
  getArtifactManifest,
  createDownload,
  deleteArtifact,
  type ClientIdentity,
  type LibraryArtifact
} from '../api';
import type { PlaybackManifest, Role } from '../types';
import { useI18n } from '../i18n';
import { Link } from '../router';
import { Player } from '../Player';
import { useConfirmDialog } from '../components/ConfirmDialog';
import { can } from '../permissions';

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

// canPreview 判定成品能否内嵌预览：
//   - mp4：文件本体可读即可（历史行同样支持，播放不需要时间轴绑定）；
//   - web_project：zip 数据包没有可打开的 HTML，预览等价于用其绑定时间轴走 Player，
//     因此要求 previewable（迁移 0040 之前的历史行无时间轴绑定，降级为仅下载）；
//   - srt/vtt：文本字幕，无播放形态，不提供预览。
function canPreview(a: LibraryArtifact): boolean {
  if (a.format === 'mp4') return a.downloadable;
  if (a.format === 'web_project') return a.previewable;
  return false;
}

type PreviewState =
  | { kind: 'video'; artifact: LibraryArtifact; url: string }
  | { kind: 'manifest'; artifact: LibraryArtifact; manifest: PlaybackManifest };

// ProjectFilter 是可搜索的项目筛选器：项目数可能远超 10，用原生 <select> 会拉出一长条
// 无法检索的列表。这里用「触发器 + 搜索框 + 可滚动列表」的组合框：输入即过滤、方向键导航、
// Esc 关闭、点击外部关闭，兼顾键盘与鼠标。
function ProjectFilter({
  options,
  value,
  onChange
}: {
  options: [string, string][];
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const rootRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const selectedLabel = value === 'all' ? t('library.allProjects') : (options.find(([id]) => id === value)?.[1] ?? value.slice(0, 8));
  const items = useMemo(() => {
    const all: [string, string][] = [['all', t('library.allProjects')], ...options];
    const q = query.trim().toLowerCase();
    return q ? all.filter(([, name]) => name.toLowerCase().includes(q)) : all;
  }, [options, query, t]);

  const choose = (id: string) => {
    onChange(id);
    setOpen(false);
    setQuery('');
  };

  return (
    <div className="project-filter" ref={rootRef}>
      <button
        type="button"
        className="library-select project-filter-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => {
          setOpen((v) => !v);
          setQuery('');
          setActive(0);
        }}
      >
        <span className="project-filter-label" title={selectedLabel}>{selectedLabel}</span>
        <span className="project-filter-caret" aria-hidden>▾</span>
      </button>
      {open && (
        <div className="project-filter-panel">
          <input
            className="project-filter-search"
            autoFocus
            placeholder={t('library.searchProject')}
            value={query}
            onChange={(e) => {
              setQuery(e.currentTarget.value);
              setActive(0);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Escape') setOpen(false);
              else if (e.key === 'ArrowDown') {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, items.length - 1));
              } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === 'Enter') {
                e.preventDefault();
                const picked = items[active] ?? items[0];
                if (picked) choose(picked[0]);
              }
            }}
          />
          <ul className="project-filter-list" role="listbox">
            {items.length === 0 ? (
              <li className="project-filter-empty">{t('library.noProjectMatch')}</li>
            ) : (
              items.map(([id, name], i) => (
                <li key={id}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={id === value}
                    className={id === value ? 'selected' : i === active ? 'active' : ''}
                    onMouseEnter={() => setActive(i)}
                    onClick={() => choose(id)}
                  >
                    {name}
                  </button>
                </li>
              ))
            )}
          </ul>
        </div>
      )}
    </div>
  );
}

// artifactSourceTitle 把成品来源渲染成「PPT 名称 + 版本」：优先用源版本展示名（sourceDisplayName），
// 缺省回退到项目名称；版本号来自 narration 写入时间轴、export 落库的 revisionNo（见 internal/app）。
// 历史成品无来源信息时退化成仅项目名称（revisionNo=0）。
function artifactSourceTitle(a: LibraryArtifact): string {
  const pptName = a.sourceDisplayName || a.projectName || a.projectId.slice(0, 8);
  return a.revisionNo ? `${pptName} v${a.revisionNo}` : pptName;
}

function IconEye() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7Z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

function IconDownload() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 3v12" />
      <path d="m7 11 5 5 5-5" />
      <path d="M5 21h14" />
    </svg>
  );
}

function IconTrash() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M3 6h18" />
      <path d="M8 6V4h8v2" />
      <path d="M6 6l1 14h10l1-14" />
    </svg>
  );
}

export function Library({ identity, role }: { identity: ClientIdentity; role?: Role }) {
  const { t } = useI18n();
  const confirmDialog = useConfirmDialog();
  const { ask: confirmAsk, dialog: confirmDialogEl } = confirmDialog;
  const canDelete = can(role, 'artifact.delete');
  const [artifacts, setArtifacts] = useState<LibraryArtifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // 筛选状态：格式 / 项目 / 时间范围。
  const [format, setFormat] = useState<string>('all');
  const [projectId, setProjectId] = useState<string>('all');
  const [range, setRange] = useState<string>('all');
  const [downloadingId, setDownloadingId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [preview, setPreview] = useState<PreviewState | null>(null);
  const [previewLoadingId, setPreviewLoadingId] = useState<string | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [artifacts, format, projectId, rangeDays]);

  // 九宫格翻页：每页 9 张；筛选条件变化时回到第一页（filtered 已按格式/项目/时间过滤）。
  const PAGE_SIZE = 9;
  const [page, setPage] = useState(0);
  useEffect(() => { setPage(0); }, [format, projectId, range]);
  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const safePage = Math.min(page, pageCount - 1);
  const pageItems = filtered.slice(safePage * PAGE_SIZE, safePage * PAGE_SIZE + PAGE_SIZE);

  // onPreview 打开内嵌预览：mp4 用签名 URL 直接喂 <video>；web_project 取成品绑定的
  // 时间轴清单喂 <Player>（与下载的 zip 同源，不会预览另一版内容）。
  const onPreview = useCallback(
    async (a: LibraryArtifact) => {
      setPreviewLoadingId(a.id);
      setPreviewError(null);
      try {
        if (a.format === 'mp4') {
          // ttl 1h：覆盖整段视频的观看时长，避免放到一半签名过期。
          const { signedUrl } = await createDownload(identity, a.id, 3600);
          setPreview({ kind: 'video', artifact: a, url: signedUrl });
        } else {
          const manifest = await getArtifactManifest(identity, a.id);
          setPreview({ kind: 'manifest', artifact: a, manifest });
        }
      } catch (e) {
        setPreviewError(e instanceof Error ? e.message : t('library.previewFailed'));
      } finally {
        setPreviewLoadingId(null);
      }
    },
    [identity, t]
  );

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

  const onDelete = useCallback(
    async (a: LibraryArtifact) => {
      const name = a.projectName || a.projectId.slice(0, 8);
      const fmt = t(FORMAT_KEY[a.format] ?? a.format);
      const ok = await confirmAsk({
        kind: 'confirm',
        titleKey: 'library.deleteTitle',
        messageKey: 'library.deleteConfirm',
        messageValues: { name, format: fmt },
        confirmKey: 'common.delete',
        danger: true
      });
      if (!ok) return;
      setDeletingId(a.id);
      setDownloadError(null);
      try {
        await deleteArtifact(identity, a.id);
        setArtifacts((cur) => cur.filter((x) => x.id !== a.id));
        setPreview((cur) => (cur && cur.artifact.id === a.id ? null : cur));
        setNotice(t('library.deleted', { name, format: fmt }));
      } catch (e) {
        setDownloadError(e instanceof Error ? e.message : t('library.deleteFailed'));
      } finally {
        setDeletingId(null);
      }
    },
    [confirmAsk, identity, t]
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
              <div className="filter-field">
                <span>{t('library.filterProject')}</span>
                <ProjectFilter options={projectOptions} value={projectId} onChange={setProjectId} />
              </div>
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
              <div>
              <ul className="library-grid">
                {pageItems.map((a) => (
                  <li className="library-card" key={a.id}>
                    <div className="library-card-head">
                      <div className="library-card-titles">
                        <span className="library-source-title" title={artifactSourceTitle(a)}>
                          {artifactSourceTitle(a)}
                        </span>
                        <span className="library-proj-title" title={a.projectName || a.projectId}>
                          {a.projectName || a.projectId.slice(0, 8)}
                        </span>
                      </div>
                      <span className={`artifact-badge artifact-${a.format}`}>{t(FORMAT_KEY[a.format] ?? a.format)}</span>
                    </div>
                    <dl className="library-card-meta">
                      <div>
                        <dt>{t('artifacts.duration')}</dt>
                        <dd>{a.durationMs > 0 ? formatDuration(a.durationMs) : t('common.none')}</dd>
                      </div>
                      <div>
                        <dt>{t('artifacts.size')}</dt>
                        <dd>{formatSize(a.sizeBytes)}</dd>
                      </div>
                      <div>
                        <dt>{t('library.createdAt')}</dt>
                        <dd>{new Date(a.createdAt).toLocaleString()}</dd>
                      </div>
                    </dl>
                    <div className="library-card-actions">
                      {canPreview(a) && (
                        <button
                          type="button"
                          className="icon-btn artifact-preview"
                          disabled={previewLoadingId === a.id}
                          aria-busy={previewLoadingId === a.id}
                          title={previewLoadingId === a.id ? t('library.previewing') : t('library.previewHint')}
                          aria-label={t('library.preview')}
                          onClick={() => void onPreview(a)}
                        >
                          <IconEye />
                        </button>
                      )}
                      <button
                        type="button"
                        className="icon-btn artifact-download"
                        disabled={!a.downloadable || downloadingId === a.id}
                        aria-busy={downloadingId === a.id}
                        title={a.downloadable ? t('artifacts.downloadHint') : t('artifacts.downloadBlocked')}
                        aria-label={t('artifacts.download')}
                        onClick={() => void onDownload(a)}
                      >
                        <IconDownload />
                      </button>
                      {canDelete && (
                        <button
                          type="button"
                          className="icon-btn danger artifact-delete"
                          disabled={deletingId === a.id}
                          aria-busy={deletingId === a.id}
                          title={deletingId === a.id ? t('library.deleting') : t('library.deleteHint')}
                          aria-label={t('library.delete')}
                          onClick={() => void onDelete(a)}
                        >
                          <IconTrash />
                        </button>
                      )}
                    </div>
                    {/* A26：不可下载时必须说明原因，不留"点了没反应"的假按钮。 */}
                    {!a.downloadable && <p className="library-card-blocked">{t('artifacts.downloadBlockedShort')}</p>}
                  </li>
                ))}
              </ul>
              {filtered.length > PAGE_SIZE && pageCount > 1 && (
                <nav className="library-pagination" aria-label={t('library.pagination')}>
                  <button
                    type="button"
                    className="button-ghost"
                    disabled={safePage === 0}
                    aria-label={t('library.prevPage')}
                    onClick={() => setPage(safePage - 1)}
                  >
                    ‹
                  </button>
                  {Array.from({ length: pageCount }, (_, i) => (
                    <button
                      key={i}
                      type="button"
                      className={i === safePage ? 'button-ghost active' : 'button-ghost'}
                      aria-current={i === safePage ? 'page' : undefined}
                      onClick={() => setPage(i)}
                    >
                      {i + 1}
                    </button>
                  ))}
                  <button
                    type="button"
                    className="button-ghost"
                    disabled={safePage >= pageCount - 1}
                    aria-label={t('library.nextPage')}
                    onClick={() => setPage(safePage + 1)}
                  >
                    ›
                  </button>
                  <span className="library-page-status">{t('library.pageStatus', { current: safePage + 1, total: pageCount })}</span>
                </nav>
              )}
              </div>
            )}
            {notice && <p className="form-notice" role="status">{notice}</p>}
            {downloadError && <p className="form-error" role="alert">{downloadError}</p>}
            {previewError && <p className="form-error" role="alert">{previewError}</p>}
          </section>
        </>
      )}

      {preview && (
        <div
          className="modal-backdrop"
          role="dialog"
          aria-modal="true"
          aria-label={t('library.previewTitle')}
          onClick={() => setPreview(null)}
        >
          {/* 阻止冒泡：点击弹层内容不应关闭。 */}
          <section className="modal-card library-preview" onClick={(e) => e.stopPropagation()}>
            <header>
              <div>
                <span className="eyebrow">{t('library.previewEyebrow')}</span>
                <h2>{preview.artifact.projectName || preview.artifact.projectId.slice(0, 8)}</h2>
              </div>
              <button type="button" onClick={() => setPreview(null)}>{t('common.close')}</button>
            </header>
            {preview.kind === 'video' ? (
              <video className="library-preview-video" src={preview.url} controls playsInline preload="metadata" />
            ) : (
              <div className="library-preview-player">
                <Player manifest={preview.manifest} embedded />
              </div>
            )}
          </section>
        </div>
      )}

      {confirmDialogEl}
    </div>
  );
}
