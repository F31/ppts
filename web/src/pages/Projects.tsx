import { useCallback, useEffect, useState } from 'react';
import {
  archiveProject,
  createProject,
  getNarration,
  getProjectSlides,
  listProjects,
  type ClientIdentity
} from '../api';
import { ImportDialog } from '../components/ImportDialog';
import { Link } from '../router';
import { type Project } from '../types';

type RowMeta = {
  slideCount: number;
  voiced: boolean;
  lastError?: string;
};

export function Projects({ identity }: { identity: ClientIdentity }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [meta, setMeta] = useState<Record<string, RowMeta>>({});
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [importTarget, setImportTarget] = useState<Project | null>(null);
  const [creating, setCreating] = useState(false);
  const [notices, setNotices] = useState<Array<{ id: string; text: string }>>([]);

  const pushNotice = (text: string) => {
    const id = `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    setNotices((current) => [...current, { id, text }].slice(-5));
  };

  const refreshMeta = useCallback(
    async (items: Project[]) => {
      const entries = await Promise.all(
        items.map(async (project) => {
          let slideCount = 0;
          let voiced = false;
          try {
            const slides = await getProjectSlides(identity, project.id);
            slideCount = slides.slides.length;
          } catch (err) {
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
        setError(err instanceof Error ? err.message : '项目加载失败');
      } finally {
        setLoading(false);
      }
    },
    [identity, refreshMeta]
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
      pushNotice(`项目「${project.title}」已创建，可导入 PPTX。`);
    } catch (err) {
      setError(err instanceof Error ? err.message : '项目创建失败');
    } finally {
      setCreating(false);
    }
  };

  const archive = async (project: Project) => {
    if (!window.confirm(`归档项目「${project.title}」？归档不等于删除；成品与配音记录仍按策略保留。`)) return;
    try {
      await archiveProject(identity, project.id);
      setProjects((current) => current.filter((item) => item.id !== project.id));
      pushNotice(`项目「${project.title}」已归档。`);
    } catch (err) {
      setError(err instanceof Error ? err.message : '归档失败');
    }
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">讲解项目</span>
          <h1>PPT 资源</h1>
        </div>
        <div className="page-actions">
          <div className="create-inline">
            <input value={title} placeholder="新项目标题" onChange={(e) => setTitle(e.currentTarget.value)} />
            <button type="button" onClick={() => void create()} disabled={creating}>
              {creating ? '创建中…' : '新建项目'}
            </button>
          </div>
          <button type="button" className="button-primary" onClick={() => void load(true)} title="刷新列表">
            刷新
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
        <header className="table-head">
          <h2>项目与导入</h2>
        </header>
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : projects.length === 0 ? (
          <div className="empty-state first-run">
            <p>还没有讲解项目。先创建一个项目，再导入第一份 PPT（PPTX，云端处理）。</p>
            <button type="button" className="button-primary" onClick={() => void create()}>
              创建第一个项目
            </button>
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>PPT 名称</th>
                <th>页数</th>
                <th>版本</th>
                <th>配音状态</th>
                <th>创建时间</th>
                <th className="col-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {projects.map((project) => {
                const row = meta[project.id];
                return (
                  <tr key={project.id}>
                    <td>
                      <Link to={`/projects/${project.id}/editor`} className="project-name" title="进入工作台">
                        {project.title}
                      </Link>
                      <small className="cell-sub">{project.id}</small>
                    </td>
                    <td>{row ? String(row.slideCount) : '—'}</td>
                    <td>{project.currentRevision}</td>
                    <td>
                      <span className={`state-tag ${row?.voiced ? 'succeeded' : 'empty'}`}>
                        {row ? (row.voiced ? '已配音' : '未配音') : '…'}
                      </span>
                    </td>
                    <td>{new Date(project.createdAtUnix * 1000).toLocaleDateString('zh-CN')}</td>
                    <td className="col-actions">
                      <div className="row-actions">
                        <Link to={`/projects/${project.id}/editor`} className="button-ghost">
                          查看
                        </Link>
                        <Link to={`/projects/${project.id}/editor?draft=1`} className="button-ghost">
                          配音
                        </Link>
                        <button type="button" onClick={() => setImportTarget(project)} title="导入 PPTX">
                          导入
                        </button>
                        <button type="button" className="danger" onClick={() => void archive(project)} title="归档">
                          归档
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </section>

      {importTarget && (
        <ImportDialog
          identity={identity}
          project={importTarget}
          onClose={() => setImportTarget(null)}
          onCompleted={() => {
            pushNotice(`已入队解析，处理完成后可在工作台编辑。`);
            void load(true);
          }}
        />
      )}
    </div>
  );
}