import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  archiveProject,
  createProject,
  getNarration,
  getProjectSlides,
  listProjects,
  type ClientIdentity
} from '../api';
import { ImportDialog } from '../components/ImportDialog';
import { useI18n } from '../i18n';
import { Link } from '../router';
import { type Project } from '../types';

type RowMeta = {
  slideCount: number;
  voiced: boolean;
  lastError?: string;
};

type SortKey = 'created_desc' | 'created_asc' | 'name_asc' | 'name_desc';
type FilterKey = 'all' | 'active' | 'archived';
type ViewKey = 'table' | 'cards';

function readView(): ViewKey {
  try {
    const v = localStorage.getItem('ppts.projects.view');
    if (v === 'table' || v === 'cards') return v;
  } catch {
    /* ignore */
  }
  return 'table';
}

// 从 URL 查询参数初始化筛选/搜索/排序状态（可分享 / 刷新保留）。
function readUrlState(): { q: string; sort: SortKey; filter: FilterKey } {
  const sp = new URLSearchParams(window.location.search);
  const q = sp.get('q') ?? '';
  const sortRaw = sp.get('sort');
  const sort: SortKey =
    sortRaw === 'created_asc' || sortRaw === 'name_asc' || sortRaw === 'name_desc'
      ? sortRaw
      : 'created_desc';
  const filterRaw = sp.get('filter');
  const filter: FilterKey = filterRaw === 'active' || filterRaw === 'archived' ? filterRaw : 'all';
  return { q, sort, filter };
}

export function Projects({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [projects, setProjects] = useState<Project[]>([]);
  const [meta, setMeta] = useState<Record<string, RowMeta>>({});
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [importTarget, setImportTarget] = useState<Project | null>(null);
  const [creating, setCreating] = useState(false);
  const [notices, setNotices] = useState<Array<{ id: string; text: string }>>([]);

  const initialUrl = useMemo(readUrlState, []);
  const [q, setQ] = useState(initialUrl.q);
  const [sort, setSort] = useState<SortKey>(initialUrl.sort);
  const [filter, setFilter] = useState<FilterKey>(initialUrl.filter);
  const [view, setView] = useState<ViewKey>(readView);

  const pushNotice = (text: string) => {
    const id = `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    setNotices((current) => [...current, { id, text }].slice(-5));
  };

  // 筛选/搜索/排序状态写回 URL（replaceState，不新增历史记录，刷新或分享链接可还原）。
  useEffect(() => {
    const sp = new URLSearchParams();
    if (q) sp.set('q', q);
    if (sort !== 'created_desc') sp.set('sort', sort);
    if (filter !== 'all') sp.set('filter', filter);
    const qs = sp.toString();
    const url = window.location.pathname + (qs ? `?${qs}` : '');
    window.history.replaceState(null, '', url);
  }, [q, sort, filter]);

  useEffect(() => {
    try {
      localStorage.setItem('ppts.projects.view', view);
    } catch {
      /* ignore */
    }
  }, [view]);

  const refreshMeta = useCallback(
    async (items: Project[]) => {
      const entries = await Promise.all(
        items.map(async (project) => {
          let slideCount = 0;
          let voiced = false;
          try {
            const slides = await getProjectSlides(identity, project.id);
            slideCount = slides.slides.length;
          } catch {
            slideCount = 0;
          }
          try {
            const narration = await getNarration(identity, project.id);
            voiced = narration.ready;
          } catch {
            voiced = false;
          }
          return [project.id, { slideCount, voiced, lastError: undefined }] as const;
        })
      );
      setMeta(Object.fromEntries(entries));
    },
    [identity]
  );

  const load = useCallback(
    async (silent = false) => {
      if (!silent) setLoading(true);
      setError('');
      try {
        const list = await listProjects(identity);
        setProjects(list);
        await refreshMeta(list);
      } catch (err) {
        setError(err instanceof Error ? err.message : t('projects.loadFailed'));
      } finally {
        setLoading(false);
      }
    },
    [identity, refreshMeta, t]
  );

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    const trimmed = title.trim();
    if (!trimmed || creating) return;
    setCreating(true);
    try {
      const project = await createProject(identity, trimmed);
      setProjects((current) => [project, ...current]);
      setTitle('');
      pushNotice(t('projects.created', { title: project.title }));
    } catch (err) {
      setError(err instanceof Error ? err.message : t('projects.createFailed'));
    } finally {
      setCreating(false);
    }
  };

  const archive = async (project: Project) => {
    if (!window.confirm(t('projects.archiveConfirm', { title: project.title }))) return;
    try {
      await archiveProject(identity, project.id);
      setProjects((current) => current.filter((item) => item.id !== project.id));
      pushNotice(t('projects.archived', { title: project.title }));
    } catch (err) {
      setError(err instanceof Error ? err.message : t('projects.archiveFailed'));
    }
  };

  const clearFilters = () => {
    setQ('');
    setSort('created_desc');
    setFilter('all');
  };

  // 客户端过滤 + 排序（基于已加载的项目列表；列表来自 List 接口，默认单页 20 条）。
  const visible = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const list = projects.filter((p) => {
      if (filter === 'active' && p.archived) return false;
      if (filter === 'archived' && !p.archived) return false;
      if (needle && !p.title.toLowerCase().includes(needle)) return false;
      return true;
    });
    return [...list].sort((a, b) => {
      switch (sort) {
        case 'created_asc':
          return a.createdAtUnix - b.createdAtUnix;
        case 'name_asc':
          return a.title.localeCompare(b.title, navigator.language);
        case 'name_desc':
          return b.title.localeCompare(a.title, navigator.language);
        case 'created_desc':
        default:
          return b.createdAtUnix - a.createdAtUnix;
      }
    });
  }, [projects, q, sort, filter]);

  const renderActions = (project: Project) => (
    <div className="row-actions">
      <Link to={`/projects/${project.id}/editor`} className="button-ghost">
        {t('projects.view')}
      </Link>
      <Link to={`/projects/${project.id}/editor?draft=1`} className="button-ghost">
        {t('projects.dub')}
      </Link>
      <button type="button" onClick={() => setImportTarget(project)} title={t('projects.import')}>
        {t('projects.import')}
      </button>
      <button
        type="button"
        className="danger"
        onClick={() => void archive(project)}
        title={t('projects.archive')}
      >
        {t('projects.archive')}
      </button>
    </div>
  );

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('projects.eyebrow')}</span>
          <h1>{t('projects.title')}</h1>
        </div>
        <div className="page-actions">
          <div className="create-inline">
            <input
              value={title}
              placeholder={t('projects.newTitle')}
              onChange={(e) => setTitle(e.currentTarget.value)}
            />
            <button type="button" onClick={() => void create()} disabled={creating}>
              {creating ? t('projects.creating') : t('projects.newProject')}
            </button>
          </div>
          <button
            type="button"
            className="button-primary"
            onClick={() => void load(true)}
            title={t('common.refresh')}
          >
            {t('common.refresh')}
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}
      {notices.map((notice) => (
        <p key={notice.id} className="floating-notice">
          {notice.text}
        </p>
      ))}

      <section className="panel">
        <header className="table-head projects-toolbar">
          <h2>{t('projects.listTitle')}</h2>
          <div className="projects-controls">
            <input
              className="projects-search"
              value={q}
              placeholder={t('projects.search')}
              onChange={(e) => setQ(e.currentTarget.value)}
              aria-label={t('projects.search')}
            />
            <label className="control-inline">
              <span>{t('projects.filterLabel')}</span>
              <select value={filter} onChange={(e) => setFilter(e.currentTarget.value as FilterKey)}>
                <option value="all">{t('projects.filterAll')}</option>
                <option value="active">{t('projects.filterActive')}</option>
                <option value="archived">{t('projects.filterArchived')}</option>
              </select>
            </label>
            <label className="control-inline">
              <span>{t('projects.sortLabel')}</span>
              <select value={sort} onChange={(e) => setSort(e.currentTarget.value as SortKey)}>
                <option value="created_desc">{t('projects.sortCreatedDesc')}</option>
                <option value="created_asc">{t('projects.sortCreatedAsc')}</option>
                <option value="name_asc">{t('projects.sortNameAsc')}</option>
                <option value="name_desc">{t('projects.sortNameDesc')}</option>
              </select>
            </label>
            <div className="view-toggle" role="group" aria-label={t('projects.viewLabel')}>
              <button
                type="button"
                className={view === 'table' ? 'active' : ''}
                onClick={() => setView('table')}
                title={t('projects.viewTable')}
              >
                {t('projects.viewTable')}
              </button>
              <button
                type="button"
                className={view === 'cards' ? 'active' : ''}
                onClick={() => setView('cards')}
                title={t('projects.viewCards')}
              >
                {t('projects.viewCards')}
              </button>
            </div>
          </div>
        </header>

        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : projects.length === 0 ? (
          <div className="empty-state first-run">
            <p>{t('projects.empty')}</p>
            <button type="button" className="button-primary" onClick={() => void create()}>
              {t('projects.createFirst')}
            </button>
          </div>
        ) : visible.length === 0 ? (
          <div className="empty-state">
            <p>{t('projects.noResults')}</p>
            <button type="button" className="button-ghost" onClick={clearFilters}>
              {t('projects.clearFilters')}
            </button>
          </div>
        ) : (
          <>
            <p className="results-count">
              {t('projects.results', { n: visible.length, total: projects.length })}
            </p>
            {view === 'table' ? (
              <table className="data-table">
                <thead>
                  <tr>
                    <th>{t('projects.colName')}</th>
                    <th>{t('projects.colSlides')}</th>
                    <th>{t('projects.colRevision')}</th>
                    <th>{t('projects.colVoice')}</th>
                    <th>{t('projects.colCreated')}</th>
                    <th className="col-actions">{t('projects.colActions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.map((project) => {
                    const row = meta[project.id];
                    return (
                      <tr key={project.id} className={project.archived ? 'row-muted' : ''}>
                        <td>
                          <Link
                            to={`/projects/${project.id}/editor`}
                            className="project-name"
                            title={t('projects.enter')}
                          >
                            {project.title}
                          </Link>
                          <small className="cell-sub">{project.id}</small>
                        </td>
                        <td>{row ? String(row.slideCount) : '—'}</td>
                        <td>{project.currentRevision}</td>
                        <td>
                          <span className={`state-tag ${row?.voiced ? 'succeeded' : 'empty'}`}>
                            {row ? (row.voiced ? t('projects.voiced') : t('projects.notVoiced')) : '…'}
                          </span>
                        </td>
                        <td>{new Date(project.createdAtUnix * 1000).toLocaleDateString()}</td>
                        <td className="col-actions">{renderActions(project)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            ) : (
              <div className="project-cards">
                {visible.map((project) => {
                  const row = meta[project.id];
                  return (
                    <div key={project.id} className={`project-card ${project.archived ? 'row-muted' : ''}`}>
                      <div className="pc-head">
                        <Link
                          to={`/projects/${project.id}/editor`}
                          className="project-name"
                          title={t('projects.enter')}
                        >
                          {project.title}
                        </Link>
                        {project.archived && <span className="state-tag empty">{t('projects.archivedBadge')}</span>}
                      </div>
                      <small className="cell-sub">{project.id}</small>
                      <div className="pc-meta">
                        <span>
                          {t('projects.colSlides')}: {row ? String(row.slideCount) : '—'}
                        </span>
                        <span>
                          {t('projects.colRevision')}: {project.currentRevision}
                        </span>
                        <span>
                          {t('projects.colVoice')}:{' '}
                          <span className={`state-tag ${row?.voiced ? 'succeeded' : 'empty'}`}>
                            {row ? (row.voiced ? t('projects.voiced') : t('projects.notVoiced')) : '…'}
                          </span>
                        </span>
                        <span>
                          {t('projects.colCreated')}: {new Date(project.createdAtUnix * 1000).toLocaleDateString()}
                        </span>
                      </div>
                      {renderActions(project)}
                    </div>
                  );
                })}
              </div>
            )}
          </>
        )}
      </section>

      {importTarget && (
        <ImportDialog
          identity={identity}
          project={importTarget}
          onClose={() => setImportTarget(null)}
          onCompleted={() => {
            pushNotice(t('projects.queued'));
            void load(true);
          }}
        />
      )}
    </div>
  );
}
