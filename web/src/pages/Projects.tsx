import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  archiveProject,
  attachTag,
  createProject,
  detachTag,
  getNarration,
  getProjectSlides,
  listFolders,
  listProjectOrganization,
  listProjects,
  listTags,
  moveProject,
  type ClientIdentity
} from '../api';
import { ImportDialog } from '../components/ImportDialog';
import { useI18n } from '../i18n';
import { Link } from '../router';
import { can, type Capability } from '../permissions';
import { type Folder, type Project, type ProjectOrg, type Role, type Tag } from '../types';

type RowMeta = {
  slideCount: number;
  voiced: boolean;
  lastError?: string;
};

type SortKey = 'created_desc' | 'created_asc' | 'name_asc' | 'name_desc';
type FilterKey = 'all' | 'active' | 'archived';
type ViewKey = 'table' | 'cards';
// 分组选择：all=全部；uncat=未分类；否则 folderId。
type FolderSel = 'all' | 'uncat' | string;

function readView(): ViewKey {
  try {
    const v = localStorage.getItem('ppts.projects.view');
    if (v === 'table' || v === 'cards') return v;
  } catch {
    /* ignore */
  }
  return 'table';
}

// 从 URL 查询参数初始化筛选/搜索/排序/分组/标签状态（可分享 / 刷新保留）。
function readUrlState(): { q: string; sort: SortKey; filter: FilterKey; folder: FolderSel; tags: string[] } {
  const sp = new URLSearchParams(window.location.search);
  const q = sp.get('q') ?? '';
  const sortRaw = sp.get('sort');
  const sort: SortKey =
    sortRaw === 'created_asc' || sortRaw === 'name_asc' || sortRaw === 'name_desc' ? sortRaw : 'created_desc';
  const filterRaw = sp.get('filter');
  const filter: FilterKey = filterRaw === 'active' || filterRaw === 'archived' ? filterRaw : 'all';
  const folderRaw = sp.get('folder');
  const folder: FolderSel = folderRaw === 'uncat' || folderRaw === '' ? 'uncat' : folderRaw ?? 'all';
  const tagsRaw = sp.get('tags');
  const tags = tagsRaw ? tagsRaw.split(',').filter(Boolean) : [];
  return { q, sort, filter, folder, tags };
}

export function Projects({
  identity,
  role,
  roleReady = true
}: {
  identity: ClientIdentity;
  role?: Role;
  roleReady?: boolean;
}) {
  const { t } = useI18n();
  const [projects, setProjects] = useState<Project[]>([]);
  const [meta, setMeta] = useState<Record<string, RowMeta>>({});
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [importTarget, setImportTarget] = useState<Project | null>(null);
  const [creating, setCreating] = useState(false);
  const [notices, setNotices] = useState<Array<{ id: string; text: string }>>([]);

  // 标签/分组组织数据。
  const [tags, setTags] = useState<Tag[]>([]);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [org, setOrg] = useState<ProjectOrg[]>([]);
  const [orgError, setOrgError] = useState('');

  const initialUrl = useMemo(readUrlState, []);
  const [q, setQ] = useState(initialUrl.q);
  const [sort, setSort] = useState<SortKey>(initialUrl.sort);
  const [filter, setFilter] = useState<FilterKey>(initialUrl.filter);
  const [folderSel, setFolderSel] = useState<FolderSel>(initialUrl.folder);
  const [selectedTags, setSelectedTags] = useState<string[]>(initialUrl.tags);
  const [view, setView] = useState<ViewKey>(readView);

  // 批量选择（打标签 / 移动分组）。
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const canOrganize = roleReady && can(role, 'project.organize' as Capability);

  const pushNotice = (text: string) => {
    const id = `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    setNotices((current) => [...current, { id, text }].slice(-5));
  };

  // 筛选/搜索/排序/分组/标签状态写回 URL（replaceState，不新增历史记录，刷新或分享链接可还原）。
  useEffect(() => {
    const sp = new URLSearchParams();
    if (q) sp.set('q', q);
    if (sort !== 'created_desc') sp.set('sort', sort);
    if (filter !== 'all') sp.set('filter', filter);
    if (folderSel !== 'all') sp.set('folder', folderSel);
    if (selectedTags.length > 0) sp.set('tags', selectedTags.join(','));
    const qs = sp.toString();
    const url = window.location.pathname + (qs ? `?${qs}` : '');
    window.history.replaceState(null, '', url);
  }, [q, sort, filter, folderSel, selectedTags]);

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

  const refreshOrg = useCallback(async () => {
    setOrgError('');
    try {
      const [tagList, folderList, orgList] = await Promise.all([
        listTags(identity),
        listFolders(identity),
        listProjectOrganization(identity)
      ]);
      setTags(tagList);
      setFolders(folderList);
      setOrg(orgList);
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : t('common.loadFailed'));
    }
  }, [identity, t]);

  const load = useCallback(
    async (silent = false) => {
      if (!silent) setLoading(true);
      setError('');
      try {
        const list = await listProjects(identity);
        setProjects(list);
        await Promise.all([refreshMeta(list), refreshOrg()]);
      } catch (err) {
        setError(err instanceof Error ? err.message : t('projects.loadFailed'));
      } finally {
        setLoading(false);
      }
    },
    [identity, refreshMeta, refreshOrg, t]
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
    setFolderSel('all');
    setSelectedTags([]);
  };

  // 项目 → 组织关联（folderId / tagIds）。
  const orgByProject = useMemo(() => {
    const m: Record<string, ProjectOrg> = {};
    for (const o of org) m[o.projectId] = o;
    return m;
  }, [org]);
  const tagById = useMemo(() => {
    const m: Record<string, Tag> = {};
    for (const tg of tags) m[tg.id] = tg;
    return m;
  }, [tags]);
  const folderById = useMemo(() => {
    const m: Record<string, Folder> = {};
    for (const f of folders) m[f.id] = f;
    return m;
  }, [folders]);

  // 分组计数（基于已加载项目）。
  const folderCounts = useMemo(() => {
    const counts: Record<string, number> = { uncat: 0 };
    for (const p of projects) {
      const o = orgByProject[p.id];
      const fid = o?.folderId ?? '';
      if (!fid) counts.uncat += 1;
      else counts[fid] = (counts[fid] ?? 0) + 1;
    }
    return counts;
  }, [projects, orgByProject]);

  // 组合过滤：已归档状态 + 搜索 + 分组 + 标签（全部命中）。
  const visible = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const list = projects.filter((p) => {
      if (filter === 'active' && p.archived) return false;
      if (filter === 'archived' && !p.archived) return false;
      if (needle && !p.title.toLowerCase().includes(needle)) return false;
      const o = orgByProject[p.id];
      const fid = o?.folderId ?? '';
      if (folderSel === 'uncat' && fid !== '') return false;
      if (folderSel !== 'all' && folderSel !== 'uncat' && fid !== folderSel) return false;
      if (selectedTags.length > 0) {
        const have = new Set(o?.tagIds ?? []);
        for (const tid of selectedTags) if (!have.has(tid)) return false;
      }
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
  }, [projects, q, sort, filter, folderSel, selectedTags, orgByProject]);

  const toggleTagFilter = (tagId: string) => {
    setSelectedTags((cur) => (cur.includes(tagId) ? cur.filter((x) => x !== tagId) : [...cur, tagId]));
  };

  const toggleSelect = (projectId: string) => {
    setSelected((cur) => {
      const next = new Set(cur);
      if (next.has(projectId)) next.delete(projectId);
      else next.add(projectId);
      return next;
    });
  };

  // 批量打标签。
  const [batchTag, setBatchTag] = useState('');
  const applyBatchTag = async () => {
    if (!batchTag || selected.size === 0) return;
    try {
      await Promise.all([...selected].map((pid) => attachTag(identity, pid, batchTag)));
      pushNotice(t('projects.batchTagged', { n: selected.size }));
      setSelected(new Set());
      await refreshOrg();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('projects.batchTagFailed'));
    }
  };

  // 批量移动分组。
  const [batchFolder, setBatchFolder] = useState('');
  const applyBatchMove = async () => {
    if (selected.size === 0) return;
    try {
      await Promise.all([...selected].map((pid) => moveProject(identity, pid, batchFolder)));
      pushNotice(t('projects.batchMoved', { n: selected.size }));
      setSelected(new Set());
      await refreshOrg();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('projects.batchMoveFailed'));
    }
  };

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

  const TagChips = ({ projectId }: { projectId: string }) => {
    const o = orgByProject[projectId];
    if (!o || o.tagIds.length === 0) return null;
    return (
      <span className="tag-chips">
        {o.tagIds.map((tid) =>
          tagById[tid] ? (
            <span key={tid} className="tag-chip" style={{ background: tagById[tid].color || '#4b5563' }}>
              {tagById[tid].name}
            </span>
          ) : null
        )}
      </span>
    );
  };

  const folderNameOf = (projectId: string): string => {
    const o = orgByProject[projectId];
    if (!o || !o.folderId) return t('folders.uncategorized');
    return folderById[o.folderId]?.name ?? t('folders.uncategorized');
  };

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
      {orgError && <p className="form-error">{orgError}</p>}
      {notices.map((notice) => (
        <p key={notice.id} className="floating-notice">
          {notice.text}
        </p>
      ))}

      <div className="projects-layout">
        <aside className="org-side" aria-label={t('projects.orgLabel')}>
          <div className="org-section">
            <h3>{t('folders.title')}</h3>
            <ul className="folder-tree">
              <li>
                <button
                  type="button"
                  className={`folder-node ${folderSel === 'all' ? 'active' : ''}`}
                  onClick={() => setFolderSel('all')}
                >
                  {t('folders.all')}
                  <span className="count">{projects.length}</span>
                </button>
              </li>
              <li>
                <button
                  type="button"
                  className={`folder-node ${folderSel === 'uncat' ? 'active' : ''}`}
                  onClick={() => setFolderSel('uncat')}
                >
                  {t('folders.uncategorized')}
                  <span className="count">{folderCounts.uncat ?? 0}</span>
                </button>
              </li>
              {folders.map((f) => (
                <li key={f.id}>
                  <button
                    type="button"
                    className={`folder-node ${folderSel === f.id ? 'active' : ''}`}
                    onClick={() => setFolderSel(f.id)}
                  >
                    📁 {f.name}
                    <span className="count">{folderCounts[f.id] ?? 0}</span>
                  </button>
                </li>
              ))}
            </ul>
          </div>
          <div className="org-section">
            <h3>{t('tags.title')}</h3>
            {tags.length === 0 ? (
              <p className="hint-note">{t('tags.emptyHint')}</p>
            ) : (
              <div className="tag-filter">
                {tags.map((tg) => (
                  <button
                    key={tg.id}
                    type="button"
                    className={`tag-chip toggle ${selectedTags.includes(tg.id) ? 'on' : ''}`}
                    style={{ background: selectedTags.includes(tg.id) ? tg.color || '#4b5563' : undefined }}
                    onClick={() => toggleTagFilter(tg.id)}
                  >
                    {tg.name}
                  </button>
                ))}
              </div>
            )}
          </div>
        </aside>

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

          {canOrganize && selected.size > 0 && (
            <div className="batch-bar">
              <span>{t('projects.selected', { n: selected.size })}</span>
              <label className="control-inline">
                <span>{t('tags.title')}</span>
                <select value={batchTag} onChange={(e) => setBatchTag(e.currentTarget.value)}>
                  <option value="">—</option>
                  {tags.map((tg) => (
                    <option key={tg.id} value={tg.id}>
                      {tg.name}
                    </option>
                  ))}
                </select>
              </label>
              <button type="button" onClick={() => void applyBatchTag()} disabled={!batchTag}>
                {t('projects.batchTag')}
              </button>
              <label className="control-inline">
                <span>{t('folders.title')}</span>
                <select value={batchFolder} onChange={(e) => setBatchFolder(e.currentTarget.value)}>
                  <option value="">{t('folders.uncategorized')}</option>
                  {folders.map((f) => (
                    <option key={f.id} value={f.id}>
                      {f.name}
                    </option>
                  ))}
                </select>
              </label>
              <button type="button" onClick={() => void applyBatchMove()}>
                {t('projects.batchMove')}
              </button>
              <button type="button" className="button-ghost" onClick={() => setSelected(new Set())}>
                {t('projects.batchClear')}
              </button>
            </div>
          )}

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
                      {canOrganize && <th className="col-check"></th>}
                      <th>{t('projects.colName')}</th>
                      <th>{t('projects.colSlides')}</th>
                      <th>{t('projects.colRevision')}</th>
                      <th>{t('projects.colVoice')}</th>
                      <th>{t('folders.title')}</th>
                      <th>{t('tags.title')}</th>
                      <th className="col-actions">{t('projects.colActions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((project) => {
                      const row = meta[project.id];
                      return (
                        <tr key={project.id} className={project.archived ? 'row-muted' : ''}>
                          {canOrganize && (
                            <td className="col-check">
                              <input
                                type="checkbox"
                                checked={selected.has(project.id)}
                                onChange={() => toggleSelect(project.id)}
                                aria-label={t('projects.select', { title: project.title })}
                              />
                            </td>
                          )}
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
                          <td>{folderNameOf(project.id)}</td>
                          <td>
                            <TagChips projectId={project.id} />
                          </td>
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
                        {canOrganize && (
                          <label className="card-check">
                            <input
                              type="checkbox"
                              checked={selected.has(project.id)}
                              onChange={() => toggleSelect(project.id)}
                              aria-label={t('projects.select', { title: project.title })}
                            />
                          </label>
                        )}
                        <div className="pc-head">
                          <Link
                            to={`/projects/${project.id}/editor`}
                            className="project-name"
                            title={t('projects.enter')}
                          >
                            {project.title}
                          </Link>
                          {project.archived && (
                            <span className="state-tag empty">{t('projects.archivedBadge')}</span>
                          )}
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
                            {t('folders.title')}: {folderNameOf(project.id)}
                          </span>
                        </div>
                        <TagChips projectId={project.id} />
                        {renderActions(project)}
                      </div>
                    );
                  })}
                </div>
              )}
            </>
          )}
        </section>
      </div>

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
