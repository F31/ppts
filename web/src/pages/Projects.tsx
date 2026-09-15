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
import { useI18n } from '../i18n';
import { Link } from '../router';
import { type Project } from '../types';

type RowMeta = {
  slideCount: number;
  voiced: boolean;
  lastError?: string;
};

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

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('projects.eyebrow')}</span>
          <h1>{t('projects.title')}</h1>
        </div>
        <div className="page-actions">
          <div className="create-inline">
            <input value={title} placeholder={t('projects.newTitle')} onChange={(e) => setTitle(e.currentTarget.value)} />
            <button type="button" onClick={() => void create()} disabled={creating}>
              {creating ? t('projects.creating') : t('projects.newProject')}
            </button>
          </div>
          <button type="button" className="button-primary" onClick={() => void load(true)} title={t('common.refresh')}>
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
        <header className="table-head">
          <h2>{t('projects.listTitle')}</h2>
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
        ) : (
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
              {projects.map((project) => {
                const row = meta[project.id];
                return (
                  <tr key={project.id}>
                    <td>
                      <Link to={`/projects/${project.id}/editor`} className="project-name" title={t('projects.enter')}>
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
                    <td className="col-actions">
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
                        <button type="button" className="danger" onClick={() => void archive(project)} title={t('projects.archive')}>
                          {t('projects.archive')}
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
            pushNotice(t('projects.queued'));
            void load(true);
          }}
        />
      )}
    </div>
  );
}