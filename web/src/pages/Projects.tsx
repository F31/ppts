import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  archiveProject,
  attachTag,
  createExport,
  createFolder,
  createProject,
  createTag,
  deleteFolder,
  deleteSourceRevision,
  detachTag,
  downloadSourceRevision,
  getPlaybackManifest,
  getRevisionNarration,
  getRevisionVoiceStatus,
  getSourceRevisions,
  listFolders,
  listProjectOrganization,
  listProjects,
  listTags,
  moveProject,
  renameFolder,
  updatePptDisplayName,
  type ClientIdentity,
  type RevisionVoiceStatus,
  type SourceRevisionSummary
} from '../api';
import { describeApiError } from '../apiError';
import { ExportDialog, type ExportOptions } from '../components/ExportDialog';
import { ImportDialog } from '../components/ImportDialog';
import { useConfirmDialog } from '../components/ConfirmDialog';
import { useI18n } from '../i18n';
import { Link, navigate } from '../router';
import { can, type Capability } from '../permissions';
import { type ArtifactFormat, type Folder, type PlaybackManifest, type Project, type ProjectOrg, type Role, type Tag } from '../types';

type ProjectVersionsState = {
  loading: boolean;
  error: string;
  revisions: SourceRevisionSummary[];
  voice: Record<number, RevisionVoiceStatus>;
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
  const confirmDialog = useConfirmDialog();
  const { ask: confirmAsk, dialog: confirmDialogEl } = confirmDialog;
  const [projects, setProjects] = useState<Project[]>([]);
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [importTarget, setImportTarget] = useState<Project | null>(null);
  const [expandedProjectId, setExpandedProjectId] = useState('');
  // 导出弹窗：目标版本 + 已构建的播放清单（就地弹 ExportDialog，不再跳到编辑器）。
  const [exportTarget, setExportTarget] = useState<{ projectId: string; revisionNo: number } | null>(null);
  const [exportManifest, setExportManifest] = useState<PlaybackManifest | null>(null);
  // 导出素材请求序号：并发点击不同项目的「导出」时，丢弃过期响应，避免"清单来自 A、
  // 目标却是 B"的错配（历史事故：产物挂 B 名下、内容来自 A，预览页图/字幕对不上）。
  const exportReqRef = useRef(0);
  const [versionsByProject, setVersionsByProject] = useState<Record<string, ProjectVersionsState>>({});
  // 每个 project+revision 的 PPT 展示名（本地暂存，刷新重置）。
  const [editableNames, setEditableNames] = useState<Record<string, Record<number, string>>>({});
  // 正在编辑的 PPT 名：key = `${projectId}:${revisionNo}`。
  const [editingCell, setEditingCell] = useState<string | null>(null);
  // 重命名失败原因（含所属项目，直接渲染在版本列表内）。
  // A26：重命名是乐观更新，失败必须回滚——否则界面留着新名字，用户以为已保存成功。
  const [renameError, setRenameError] = useState<{ projectId: string; message: string } | null>(null);
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

  // 内联创建（分组 / 标签）：在项目页左栏直接创建，无需跳转设置页。
  const [showFolderComposer, setShowFolderComposer] = useState(false);
  const [folderDraft, setFolderDraft] = useState('');
  const [folderAfterId, setFolderAfterId] = useState('');
  // 内联重命名：哪个分组正在编辑名称（空 = 无）。
  const [editingFolderId, setEditingFolderId] = useState('');
  const [editFolderDraft, setEditFolderDraft] = useState('');
  const [showTagComposer, setShowTagComposer] = useState(false);
  const [tagDraft, setTagDraft] = useState('');
  const [tagColorDraft, setTagColorDraft] = useState('#2563eb');

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
        await refreshOrg();
      } catch (err) {
        setError(err instanceof Error ? err.message : t('projects.loadFailed'));
      } finally {
        setLoading(false);
      }
    },
    [identity, refreshOrg, t]
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
    const ok = await confirmAsk({ kind: 'confirm', titleKey: 'projects.archiveTitle', messageKey: 'projects.archiveConfirm', messageValues: { title: project.title }, confirmKey: 'projects.archive', danger: true });
    if (!ok) return;
    try {
      await archiveProject(identity, project.id);
      setProjects((current) => current.filter((item) => item.id !== project.id));
      pushNotice(t('projects.archived', { title: project.title }));
    } catch (err) {
      setError(err instanceof Error ? err.message : t('projects.archiveFailed'));
    }
  };

  const fetchVersions = useCallback(
    async (project: Project) => {
      setVersionsByProject((current) => ({
        ...current,
        [project.id]: { loading: true, error: '', revisions: [], voice: {} }
      }));
      try {
        const [result, voice] = await Promise.all([
          getSourceRevisions(identity, project.id),
          getRevisionVoiceStatus(identity, project.id)
        ]);
        const voiceMap = Object.fromEntries((voice.revisions ?? []).map((item) => [item.revisionNo, item]));
        setVersionsByProject((current) => ({
          ...current,
          [project.id]: { loading: false, error: '', revisions: result.revisions, voice: voiceMap }
        }));
      } catch (err) {
        setVersionsByProject((current) => ({
          ...current,
          [project.id]: {
            loading: false,
            error: err instanceof Error ? err.message : t('projects.versionsFailed'),
            revisions: [],
            voice: {}
          }
        }));
      }
    },
    [identity, t]
  );

  const toggleVersions = async (project: Project) => {
    if (expandedProjectId === project.id) {
      setExpandedProjectId('');
      return;
    }
    setExpandedProjectId(project.id);
    if (versionsByProject[project.id]) return;
    void fetchVersions(project);
  };

  const clearFilters = () => {
    setQ('');
    setSort('created_desc');
    setFilter('all');
    setFolderSel('all');
    setSelectedTags([]);
  };

  // 就地导出：读该版本的配音状态 → 构建播放清单 → 弹 ExportDialog（不再跳去编辑器）。
  // ExportDialog 需要 manifest（页图数/快照尾），因此先取素材再弹窗；失败可视并给原因。
  const [exportBusy, setExportBusy] = useState(false);
  const [exportError, setExportError] = useState('');
  // 按 slide 对齐的页图键（缺图位置为空串），经 getRevisionNarration 取得，导出时透传给后端做缺图降级。
  const [exportPagePngKeys, setExportPagePngKeys] = useState<string[]>([]);
  const openExport = async (projectId: string, revisionNo: number) => {
    const reqId = ++exportReqRef.current;
    setExportError('');
    setExportBusy(true);
    try {
      const status = await getRevisionNarration(identity, projectId, revisionNo);
      if (reqId !== exportReqRef.current) return;
      if (!status.ready || !status.timelineKey) throw new Error(t('projects.exportNoNarration'));
      setExportPagePngKeys(status.pagePngKeys ?? []);
      const manifest = await getPlaybackManifest({
        identity,
        projectId,
        timelineKey: status.timelineKey,
        pagePngKeys: status.pagePngKeys ?? [],
        ttlSeconds: 900
      });
      if (reqId !== exportReqRef.current) return;
      // 双保险：清单必须与请求的项目 + 时间轴同源，否则拒绝（防止并发/缓存错配）。
      if (manifest.projectId !== projectId || manifest.timelineKey !== status.timelineKey) {
        throw new Error(t('projects.exportPrepareFailed'));
      }
      setExportManifest(manifest);
      setExportTarget({ projectId, revisionNo });
    } catch (err) {
      if (reqId !== exportReqRef.current) return;
      setExportManifest(null);
      setExportError(describeApiError(err, t('projects.exportPrepareFailed'), t));
      setExportTarget({ projectId, revisionNo });
    } finally {
      if (reqId === exportReqRef.current) setExportBusy(false);
    }
  };

  const runExport = async (format: ArtifactFormat, options: ExportOptions) => {
    if (!exportManifest || !exportTarget) return;
    setExportBusy(true);
    setExportError('');
    try {
      // 透传「按 slide 对齐」的页图键（缺图位置为空串）；后端据此缺图降级，而非因一页缺失整单失败。
      const pagePngKeys = format === 'ARTIFACT_FORMAT_MP4' ? exportPagePngKeys : [];
      const result = await createExport(identity, {
        projectId: exportTarget.projectId,
        format,
        timelineKey: exportManifest.timelineKey,
        pagePngKeys: format === 'ARTIFACT_FORMAT_MP4' ? pagePngKeys : [],
        burnSubtitles: format === 'ARTIFACT_FORMAT_MP4' ? options.burnSubtitles : undefined,
        includeNotes: options.includeNotes,
        idempotencyKey: `export-${exportTarget.projectId}-${Date.now()}`
      });
      setExportTarget(null);
      setExportManifest(null);
      pushNotice(t('projects.exportQueued', { jobId: result.jobId }));
    } catch (err) {
      setExportError(err instanceof Error ? err.message : t('editor.exportFailed'));
    } finally {
      setExportBusy(false);
    }
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

  // 内联创建分组。
  const submitFolder = async () => {
    const name = folderDraft.trim();
    if (!name) return;
    try {
      const folder = await createFolder(identity, name, folderAfterId);
      setFolders((cur) => [...cur, folder].sort((a, b) => (a.sortOrder ?? 0) - (b.sortOrder ?? 0) || a.name.localeCompare(b.name)));
      setFolderDraft('');
      setFolderAfterId('');
      setShowFolderComposer(false);
      pushNotice(t('folders.created', { name: folder.name }));
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : t('common.saveFailed'));
    }
  };

  // 内联创建标签。
  const submitTag = async () => {
    const name = tagDraft.trim();
    if (!name) return;
    try {
      const tag = await createTag(identity, name, tagColorDraft);
      setTags((cur) => [...cur, tag]);
      setTagDraft('');
      setShowTagComposer(false);
      pushNotice(t('tags.created', { name: tag.name }));
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : t('common.saveFailed'));
    }
  };

  // 行内移动分组（单项目）。
  const moveToFolder = async (projectId: string, folderId: string) => {
    try {
      await moveProject(identity, projectId, folderId);
      setOrg((cur) => {
        const rest = cur.filter((o) => o.projectId !== projectId);
        const prev = cur.find((o) => o.projectId === projectId);
        return [...rest, { projectId, folderId, tagIds: prev?.tagIds ?? [] }];
      });
      pushNotice(t('projects.folderMoved'));
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : t('projects.batchMoveFailed'));
    }
  };

  // 行内打标签（单项目，追加）。
  const addTagToProject = async (projectId: string, tagId: string) => {
    if (!tagId) return;
    try {
      await attachTag(identity, projectId, tagId);
      setOrg((cur) => {
        const prev = cur.find((o) => o.projectId === projectId);
        const rest = cur.filter((o) => o.projectId !== projectId);
        const tagIds = prev?.tagIds ?? [];
        return [...rest, { projectId, folderId: prev?.folderId ?? '', tagIds: tagIds.includes(tagId) ? tagIds : [...tagIds, tagId] }];
      });
      pushNotice(t('projects.tagAdded'));
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : t('projects.batchTagFailed'));
    }
  };

  // 行内移除标签（单项目）。
  const removeTagFromProject = async (projectId: string, tagId: string) => {
    try {
      await detachTag(identity, projectId, tagId);
      setOrg((cur) => {
        const prev = cur.find((o) => o.projectId === projectId);
        if (!prev) return cur;
        const rest = cur.filter((o) => o.projectId !== projectId);
        return [...rest, { ...prev, tagIds: prev.tagIds.filter((x) => x !== tagId) }];
      });
      pushNotice(t('projects.tagRemoved'));
    } catch (err) {
      setOrgError(err instanceof Error ? err.message : t('projects.batchTagFailed'));
    }
  };

  const renderProjectName = (project: Project) => (
    <button
      type="button"
      className="project-name-button"
      onClick={() => void toggleVersions(project)}
      aria-expanded={expandedProjectId === project.id}
      title={t('projects.showPpts')}
    >
      <span className="project-name">{project.title}</span>
      <small className="cell-sub">{project.id}</small>
    </button>
  );

  // 重命名失败的提示紧邻版本列表渲染：页面顶部的通用错误条在展开行之后往往已滚出视口。
  // 表格视图与卡片视图都调用本函数，避免两处各写一遍 markup 而漂移。
  const renderRenameError = (project: Project) =>
    renameError?.projectId === project.id ? (
      <div className="load-failure" role="alert">
        <p className="form-error">{renameError.message}</p>
      </div>
    ) : null;

  const renderVersionList = (project: Project) => {
    const state = versionsByProject[project.id];
    if (!state || state.loading) return <p className="cell-sub">{t('common.loading')}</p>;
    if (state.error) return <p className="api-status error" role="alert">{state.error}</p>;
    if (state.revisions.length === 0) return <p className="cell-sub">{t('projects.noPpts')}</p>;
    const editKey = (revNo: number) => `${project.id}:${revNo}`;
    const startEdit = (revNo: number) => setEditingCell(editKey(revNo));
    const commitEdit = (revNo: number, newName: string) => {
      const previous = editableNames[project.id]?.[revNo];
      setEditableNames((cur) => {
        const projMap = cur[project.id] ?? {};
        return { ...cur, [project.id]: { ...projMap, [revNo]: newName } };
      });
      setEditingCell(null);
      setRenameError(null);
      // 持久化到后端（异步，不阻塞 UI）。失败必须回滚乐观更新，否则是"假成功"：
      // 界面留着新名字，只有刷新才会悄悄退回旧值（A26 禁止的形态）。
      void updatePptDisplayName(identity, project.id, revNo, newName)
        .catch((err: unknown) => {
          setEditableNames((cur) => {
            const projMap = cur[project.id];
            if (!projMap) return cur;
            const next = { ...projMap };
            if (previous === undefined) delete next[revNo];
            else next[revNo] = previous;
            return { ...cur, [project.id]: next };
          });
          setRenameError({ projectId: project.id, message: describeApiError(err, t('projects.renameFailed'), t) });
        });
    };
    const getDisplayName = (rev: SourceRevisionSummary) =>
      (editableNames[project.id]?.[rev.revisionNo] ?? rev.displayName) || rev.displayName;
    const statusClass = (status?: RevisionVoiceStatus) => {
      if (!status) return 'empty';
      if (status.status === 'complete') return 'succeeded';
      if (status.status === 'partial' || status.status === 'stale') return 'queued';
      return 'empty';
    };
    const statusLabel = (status?: RevisionVoiceStatus) => {
      if (!status) return '…';
      const values = { voiced: status.voicedPages, total: status.pageCount, missing: status.missingPages, stale: status.stalePages };
      switch (status.status) {
        case 'complete':
          return t('projects.voiceComplete', values);
        case 'partial':
          return t('projects.voicePartial', values);
        case 'stale':
          return t('projects.voiceStale', values);
        default:
          return t('projects.voiceNotVoiced', values);
      }
    };
    const statusTitle = (status?: RevisionVoiceStatus) => {
      if (!status) return '';
      return t('projects.voiceStatusDetail', {
        voiced: status.voicedPages,
        total: status.pageCount,
        missing: status.missingPages,
        stale: status.stalePages
      });
    };
    const downloadRevision = async (rev: SourceRevisionSummary) => {
      try {
        await downloadSourceRevision(identity, project.id, rev.revisionNo, getDisplayName(rev));
      } catch (err) {
        setError(err instanceof Error ? err.message : t('projects.downloadFailed'));
      }
    };
    return (
      <table className="data-table nested-table">
        <thead>
          <tr>
            <th className="col-ppt-name">{t('projects.colPptName')}</th>
            <th>{t('projects.colSlides')}</th>
            <th>{t('projects.colRevision')}</th>
            <th>{t('projects.colCreated')}</th>
            <th>{t('projects.colVoice')}</th>
            <th>{t('tags.title')}</th>
            <th className="col-actions">{t('projects.colActions')}</th>
          </tr>
        </thead>
        <tbody>
          {state.revisions.map((revision) => {
            const isEditing = editingCell === editKey(revision.revisionNo);
            const voiceStatus = state.voice[revision.revisionNo];
            const canExportRevision = voiceStatus?.status === 'complete';
            return (
            <tr key={revision.revisionNo}>
              <td className="col-ppt-name">
                {isEditing ? (
                    <input
                      className="ppt-name-input"
                      defaultValue={getDisplayName(revision)}
                      autoFocus
                      onBlur={(e) => commitEdit(revision.revisionNo, e.currentTarget.value.trim())}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') e.currentTarget.blur();
                        if (e.key === 'Escape') setEditingCell(null);
                      }}
                    />
                  ) : (
                    <span className="ppt-name-wrap">
                      <button
                        type="button"
                        className="ppt-name"
                        title={t('projects.viewPpt')}
                        onClick={() => navigate(`/projects/${project.id}/editor${revision.isCurrent ? '' : `?rev=${revision.revisionNo}`}`)}
                      >
                        {getDisplayName(revision)}
                      </button>
                      <button
                        type="button"
                        className="ppt-name-edit"
                        title={t('projects.editPptName')}
                        aria-label={t('projects.editPptName')}
                        onClick={(e) => {
                          e.stopPropagation();
                          startEdit(revision.revisionNo);
                        }}
                      >
                        ✎
                      </button>
                    </span>
                  )}
                </td>
                <td>{revision.pageCount}</td>
                <td>v{revision.revisionNo}</td>
                <td>{new Date(revision.createdAt).toLocaleString()}</td>
                <td>
                  <span className={`state-tag ${statusClass(voiceStatus)}`} title={statusTitle(voiceStatus)}>
                    {statusLabel(voiceStatus)}
                  </span>
                </td>
                <td>
                  <TagChips projectId={project.id} />
                </td>
                <td className="col-actions">
                  <button
                    type="button"
                    className="button-ghost"
                    onClick={() => navigate(`/projects/${project.id}/editor${revision.isCurrent ? '' : `?rev=${revision.revisionNo}`}`)}
                    title={t('projects.view')}
                  >
                    {t('projects.view')}
                  </button>
                  {canExportRevision ? (
                    <button
                      type="button"
                      className="button-ghost"
                      onClick={() => void openExport(project.id, revision.revisionNo)}
                      title={t('projects.export')}
                    >
                      {t('projects.export')}
                    </button>
                  ) : (
                    <button
                      type="button"
                      className="button-ghost"
                      onClick={() => void downloadRevision(revision)}
                      title={t('projects.downloadPpt')}
                    >
                      {t('projects.downloadPpt')}
                    </button>
                  )}
                  <button
                    type="button"
                    className="button-ghost danger"
                    onClick={async () => {
                      const ok = await confirmAsk({
                        kind: 'confirm',
                        titleKey: revision.isCurrent ? 'projects.deleteCurrentPptTitle' : 'projects.deletePptTitle',
                        messageKey: revision.isCurrent ? 'projects.deleteCurrentPptConfirm' : 'projects.deletePptConfirm',
                        messageValues: { name: getDisplayName(revision), no: revision.revisionNo },
                        confirmKey: 'common.delete',
                        danger: true
                      });
                      if (!ok) return;
                      try {
                        await deleteSourceRevision(identity, project.id, revision.revisionNo);
                        pushNotice(t('projects.pptDeleted', { name: getDisplayName(revision) }));
                        // 删除当前版本会回退到上一版：刷新项目（current_revision 变化）与该项目的版本列表。
                        void load(true);
                        void fetchVersions(project);
                      } catch (err) {
                        const msg = err instanceof Error ? err.message : t('projects.deletePptFailed');
                        if (msg.includes('only revision') || msg.includes('唯一')) setError(t('projects.deleteLastBlocked'));
                        else if (msg.includes('current revision') || msg.includes('当前生效版本')) setError(t('projects.deleteCurrentBlocked'));
                        else setError(msg);
                      }
                    }}
                    title={t('projects.deletePpt')}
                  >
                    {t('projects.deletePpt')}
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    );
  };

  const renderActions = (project: Project) => (
    <div className="row-actions">
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

  const TagChips = ({ projectId, onRemove }: { projectId: string; onRemove?: (tagId: string) => void }) => {
    const o = orgByProject[projectId];
    if (!o || o.tagIds.length === 0) return null;
    return (
      <span className="tag-chips">
        {o.tagIds.map((tid) =>
          tagById[tid] ? (
            <span key={tid} className="tag-chip" style={{ background: tagById[tid].color || '#4b5563' }}>
              {tagById[tid].name}
              {onRemove && (
                <button
                  type="button"
                  className="chip-x"
                  onClick={() => onRemove(tid)}
                  title={t('projects.removeTag')}
                  aria-label={t('projects.removeTag')}
                >
                  ×
                </button>
              )}
            </span>
          ) : null
        )}
      </span>
    );
  };

  // 行内标签控件：已挂标签（可移除）+ 「＋」下拉追加。
  const TagControl = ({ projectId }: { projectId: string }) => {
    const attached = new Set(orgByProject[projectId]?.tagIds ?? []);
    const options = tags.filter((tg) => !attached.has(tg.id));
    return (
      <div className="tag-cell">
        <TagChips projectId={projectId} onRemove={(tid) => void removeTagFromProject(projectId, tid)} />
        {canOrganize && (
          <select
            className="inline-select tag-add"
            value=""
            onChange={(e) => {
              void addTagToProject(projectId, e.currentTarget.value);
              e.currentTarget.value = '';
            }}
            title={t('projects.addTag')}
            aria-label={t('projects.addTag')}
          >
            <option value="">＋</option>
            {options.map((tg) => (
              <option key={tg.id} value={tg.id}>
                {tg.name}
              </option>
            ))}
          </select>
        )}
      </div>
    );
  };

  // 行内分组控件：下拉直接移动（未分类 + 各分组）。
  const FolderControl = ({ projectId }: { projectId: string }) => {
    if (!canOrganize) return <>{folderNameOf(projectId)}</>;
    return (
      <select
        className="inline-select"
        value={orgByProject[projectId]?.folderId ?? ''}
        onChange={(e) => void moveToFolder(projectId, e.currentTarget.value)}
        title={t('projects.moveFolder')}
        aria-label={t('projects.moveFolder')}
      >
        <option value="">{t('folders.uncategorized')}</option>
        {folders.map((f) => (
          <option key={f.id} value={f.id}>
            {f.name}
          </option>
        ))}
      </select>
    );
  };

  const folderNameOf = (projectId: string): string => {
    const o = orgByProject[projectId];
    if (!o || !o.folderId) return t('folders.uncategorized');
    return folderById[o.folderId]?.name ?? t('folders.uncategorized');
  };

  // 左栏分组节点：点击切换筛选；hover 时显示重命名/删除按钮（仅 editor+）。
  const FolderNode = ({ folder }: { folder: Folder }) => {
    const isActive = folderSel === folder.id;
    const count = folderCounts[folder.id] ?? 0;
    const isEditing = editingFolderId === folder.id;
    const startEdit = () => {
      setEditingFolderId(folder.id);
      setEditFolderDraft(folder.name);
    };
    const cancelEdit = () => {
      setEditingFolderId('');
      setEditFolderDraft('');
    };
    const submitRename = async () => {
      const name = editFolderDraft.trim();
      if (!name || name === folder.name) { cancelEdit(); return; }
      try {
        await renameFolder(identity, folder.id, name);
        pushNotice(t('folders.renamed', { name }));
        await refreshOrg();
        cancelEdit();
      } catch {
        pushNotice(t('folders.renameFailed'));
        cancelEdit();
      }
    };
    const handleDelete = async () => {
      if (count > 0) {
        pushNotice(t('folders.deleteBlocked'));
        return;
      }
      const ok = await confirmAsk({ kind: 'confirm', titleKey: 'folders.deleteTitle', messageKey: 'folders.deleteConfirm', messageValues: { name: folder.name }, confirmKey: 'common.delete', danger: true });
      if (!ok) return;
      try {
        await deleteFolder(identity, folder.id);
        pushNotice(t('folders.deleted', { name: folder.name }));
        await refreshOrg();
        if (folderSel === folder.id) setFolderSel('all');
      } catch (err) {
        const msg = err instanceof Error ? err.message : t('folders.deleteFailed');
        pushNotice(msg);
      }
    };
    return (
      <li>
        <button
          type="button"
          className={`folder-node ${isActive ? 'active' : ''}`}
          onClick={() => setFolderSel(folder.id)}
          aria-label={`${t('folders.title')}：${folder.name}，${count} 个项目`}
        >
          <span className="folder-icon">📁</span>
          {isEditing ? (
            <input
              className="folder-rename-input"
              value={editFolderDraft}
              onChange={(e) => setEditFolderDraft(e.currentTarget.value)}
              onBlur={submitRename}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void submitRename();
                if (e.key === 'Escape') cancelEdit();
              }}
              autoFocus
              maxLength={40}
            />
          ) : (
            <span className="folder-label">{folder.name}</span>
          )}
          <span className="count">{count}</span>
          {canOrganize && (
            <span className="folder-actions" title={t('common.actions')}>
              <button
                type="button"
                className="icon-btn"
                onClick={(e) => { e.stopPropagation(); startEdit(); }}
                aria-label={t('common.rename')}
                title={t('common.rename')}
              >✏️</button>
              <button
                type="button"
                className={`icon-btn ${count > 0 ? 'disabled' : ''}`}
                onClick={(e) => { e.stopPropagation(); void handleDelete(); }}
                aria-label={t('common.delete')}
                title={count > 0 ? t('folders.deleteBlocked') : t('common.delete')}
                disabled={count > 0}
              >🗑️</button>
            </span>
          )}
        </button>
      </li>
    );
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

      {/* 错误必须播报（role="alert"）；通知是信息性内容，用 polite 的 live region，
          避免与错误抢屏（全仓仅此三处通知，此前都是裸 <p>）。 */}
      {error && <p className="form-error" role="alert">{error}</p>}
      {orgError && <p className="form-error" role="alert">{orgError}</p>}
      {notices.map((notice) => (
        <p key={notice.id} className="floating-notice" role="status" aria-live="polite">
          {notice.text}
        </p>
      ))}

      <div className="projects-layout">
        <aside className="org-side" aria-label={t('projects.orgLabel')}>
          <div className="org-section">
            <div className="org-head">
              <h3>{t('folders.title')}</h3>
              {canOrganize && (
                <button
                  type="button"
                  className="link-btn"
                  onClick={() => setShowFolderComposer((v) => !v)}
                  title={t('folders.create')}
                >
                  ＋ {t('folders.create')}
                </button>
              )}
            </div>
            {canOrganize && showFolderComposer && (
              <form
                className="org-composer"
                onSubmit={(e) => {
                  e.preventDefault();
                  void submitFolder();
                }}
              >
                <input
                  value={folderDraft}
                  onChange={(e) => setFolderDraft(e.currentTarget.value)}
                  placeholder={t('folders.newName')}
                  autoFocus
                />
                <label className="sr-only" htmlFor="folder-after">
                  {t('folders.position')}
                </label>
                <select id="folder-after" value={folderAfterId} onChange={(e) => setFolderAfterId(e.currentTarget.value)}>
                  <option value="">{t('folders.positionEnd')}</option>
                  {folders.map((folder) => (
                    <option key={folder.id} value={folder.id}>
                      {t('folders.positionAfter', { name: folder.name })}
                    </option>
                  ))}
                </select>
                <button type="submit" disabled={!folderDraft.trim()}>
                  ✓
                </button>
              </form>
            )}
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
                <FolderNode key={f.id} folder={f} />
              ))}
            </ul>
          </div>
          <div className="org-section">
            <div className="org-head">
              <h3>{t('tags.title')}</h3>
              {canOrganize && (
                <button
                  type="button"
                  className="link-btn"
                  onClick={() => setShowTagComposer((v) => !v)}
                  title={t('tags.create')}
                >
                  ＋ {t('tags.create')}
                </button>
              )}
            </div>
            {canOrganize && showTagComposer && (
              <form
                className="org-composer"
                onSubmit={(e) => {
                  e.preventDefault();
                  void submitTag();
                }}
              >
                <input
                  value={tagDraft}
                  onChange={(e) => setTagDraft(e.currentTarget.value)}
                  placeholder={t('tags.newName')}
                  autoFocus
                />
                <input
                  type="color"
                  value={tagColorDraft}
                  onChange={(e) => setTagColorDraft(e.currentTarget.value)}
                  title={t('tags.color')}
                  aria-label={t('tags.color')}
                />
                <button type="submit" disabled={!tagDraft.trim()}>
                  ✓
                </button>
              </form>
            )}
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
          {canOrganize && (
            <div className="org-section">
              <Link to="/settings/tags" className="button-ghost org-manage">
                {t('projects.manageOrg')}
              </Link>
            </div>
          )}
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
                      <th>{t('projects.colProjectName')}</th>
                      <th>{t('folders.title')}</th>
                      <th>{t('tags.title')}</th>
                      <th className="col-actions">{t('projects.colActions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((project) => {
                      return (
                        <Fragment key={project.id}>
                          <tr className={project.archived ? 'row-muted' : ''}>
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
                            <td>{renderProjectName(project)}</td>
                            <td>
                              <FolderControl projectId={project.id} />
                            </td>
                            <td>
                              <TagControl projectId={project.id} />
                            </td>
                            <td className="col-actions">{renderActions(project)}</td>
                          </tr>
                          {expandedProjectId === project.id && (
                            <tr className="project-detail-row">
                              <td colSpan={canOrganize ? 5 : 4}>{renderRenameError(project)}{renderVersionList(project)}</td>
                            </tr>
                          )}
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              ) : (
                <div className="project-cards">
                  {visible.map((project) => {
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
                          {renderProjectName(project)}
                          {project.archived && (
                            <span className="state-tag empty">{t('projects.archivedBadge')}</span>
                          )}
                        </div>
                        <div className="pc-meta">
                          <span className="pc-org">
                            {t('folders.title')}: <FolderControl projectId={project.id} />
                          </span>
                        </div>
                        <TagControl projectId={project.id} />
                        {expandedProjectId === project.id && <div className="project-card-detail">{renderRenameError(project)}{renderVersionList(project)}</div>}
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
            const pid = importTarget.id;
            // 导入会新增一个源版本：失效该项目已缓存的版本列表；若正展开则立即重载，
            // 否则列表要等手动刷新才出现新版本（本 bug）。
            setVersionsByProject((current) => {
              if (!(pid in current)) return current;
              const next = { ...current };
              delete next[pid];
              return next;
            });
            void load(true);
            if (expandedProjectId === pid) void fetchVersions(importTarget);
          }}
        />
      )}
      {exportTarget && exportManifest && (
        <ExportDialog
          manifest={exportManifest}
          busy={exportBusy}
          error={exportError}
          onClose={() => {
            setExportTarget(null);
            setExportError('');
          }}
          onSubmit={(format, options) => void runExport(format, options)}
        />
      )}
      {confirmDialogEl}
    </div>
  );
}
