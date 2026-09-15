import { useCallback, useEffect, useState } from 'react';
import {
  getNarration,
  getStorageUsage,
  getUsage,
  listGateways,
  listJobs,
  listProjects,
  type ClientIdentity
} from '../api';
import { useI18n } from '../i18n';
import { Link } from '../router';
import { jobKindKey, jobStateKey, type Job, type JobState, type Project } from '../types';

const activeStates: JobState[] = ['JOB_STATE_QUEUED', 'JOB_STATE_RUNNING', 'JOB_STATE_RETRY_WAIT', 'JOB_STATE_CANCEL_REQUESTED', 'JOB_STATE_UNKNOWN_PROVIDER_RESULT'];

function fmtBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[unit]}`;
}

export function Home({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const [projects, setProjects] = useState<Project[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [voiceReady, setVoiceReady] = useState<Record<string, boolean>>({});
  const [stats, setStats] = useState<{ usageSeconds: number; storageBytes: number; ttsConfigured: boolean; llmConfigured: boolean }>({
    usageSeconds: 0,
    storageBytes: 0,
    ttsConfigured: false,
    llmConfigured: false
  });
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [projectList, jobList] = await Promise.all([listProjects(identity), listJobs(identity)]);
      setProjects(projectList);
      setJobs(jobList);

      const [usage, storage] = await Promise.all([
        getUsage(identity).catch(() => ({ secondsUsed: 0 })),
        getStorageUsage(identity).catch(() => ({ totalBytes: 0 }))
      ]);
      let tts = false;
      let llm = false;
      try {
        const gateways = await listGateways(identity);
        tts = gateways.some((g) => g.kind === 'tts' && g.enabled);
        llm = gateways.some((g) => g.kind === 'llm' && g.enabled);
      } catch {
        // 非 admin 无法读取模型服务；不阻塞首页。
      }
      setStats({
        usageSeconds: usage.secondsUsed ?? 0,
        storageBytes: storage.totalBytes ?? 0,
        ttsConfigured: tts,
        llmConfigured: llm
      });

      const readyMap: Record<string, boolean> = {};
      const recent = projectList.slice(0, 6);
      await Promise.all(
        recent.map(async (project) => {
          try {
            const narration = await getNarration(identity, project.id);
            readyMap[project.id] = narration.ready;
          } catch {
            readyMap[project.id] = false;
          }
        })
      );
      setVoiceReady(readyMap);
      setLoading(false);
    } finally {
      setLoading(false);
    }
  }, [identity]);

  useEffect(() => {
    void load();
  }, [load]);

  const activeJobCount = jobs.filter((job) => activeStates.includes(job.state)).length;
  const voicedCount = Object.values(voiceReady).filter(Boolean).length;
  const fmtSeconds = (seconds: number) =>
    seconds < 60 ? t('home.seconds', { n: seconds }) : t('home.minutes', { n: Math.round((seconds / 60) * 10) / 10 });

  return (
    <div className="page-stack">
      <section className="home-hero">
        <div>
          <span className="eyebrow">{t('home.eyebrow')}</span>
          <h1>{t('home.welcome', { name: (identity.userId ?? t('home.user')).slice(0, 24) })}</h1>
          <p>{t('home.subtitle')}</p>
        </div>
      </section>

      <section className="stat-grid" aria-label={t('home.eyebrow')}>
        <StatCard label={t('home.stat.projects')} value={String(projects.length)} note={t('home.stat.projectsNote')} />
        <StatCard label={t('home.stat.voiced')} value={String(voicedCount)} note={t('home.stat.voicedNote')} />
        <StatCard label={t('home.stat.activeJobs')} value={String(activeJobCount)} note={t('home.stat.activeJobsNote')} />
        <StatCard label={t('home.stat.usage')} value={fmtSeconds(stats.usageSeconds)} note={t('home.stat.usageNote')} />
        <StatCard label={t('home.stat.storage')} value={fmtBytes(stats.storageBytes)} note={t('home.stat.storageNote')} />
        <StatCard
          label={t('home.stat.models')}
          value={stats.ttsConfigured && stats.llmConfigured ? t('home.models.configured') : stats.ttsConfigured || stats.llmConfigured ? t('home.models.partial') : t('home.models.none')}
          note={t('home.stat.modelsNote')}
        />
      </section>
      {!loading && <p className="stats-note">{t('home.statsNote')}</p>}

      <section className="panel home-section">
        <header>
          <h2>{t('home.recentProjects')}</h2>
          <Link to="/projects" className="link-more">{t('home.viewAll')}</Link>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : projects.length === 0 ? (
          <div className="empty-state first-run">
            <p>{t('home.noProjects')}</p>
            <Link to="/projects" className="button-primary">{t('home.importFirst')}</Link>
          </div>
        ) : (
          <div className="recent-projects">
            {projects.slice(0, 6).map((project) => (
              <Link key={project.id} to={`/projects/${project.id}/editor`} className="project-card">
                <strong>{project.title}</strong>
                <span>rev {project.currentRevision}</span>
                <em>{voiceReady[project.id] ? t('home.voicedPlayable') : t('home.noVoice')}</em>
              </Link>
            ))}
          </div>
        )}
      </section>

      <section className="panel home-section">
        <header>
          <h2>{t('home.recentJobs')}</h2>
          <Link to="/jobs" className="link-more">{t('home.jobsCenter')}</Link>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : jobs.length === 0 ? (
          <p className="empty-state">{t('home.noJobs')}</p>
        ) : (
          <div className="jobs-compact">
            {jobs.slice(0, 6).map((job) => (
              <Link key={job.jobId} to={`/jobs?job=${job.jobId}`} className="job-row">
                <span className={`state-tag ${job.state.toLowerCase()}`}>{t(jobStateKey[job.state])}</span>
                <strong>{jobKindKey[job.kind] ? t(jobKindKey[job.kind]) : job.kind}</strong>
                <span className="job-progress">{job.progressPercent >= 0 ? t('home.progress', { percent: job.progressPercent }) : ''}</span>
                <small>{new Date(job.updatedAtUnix * 1000).toLocaleString()}</small>
              </Link>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function StatCard({ label, value, note }: { label: string; value: string; note: string }) {
  return (
    <div className="stat-card">
      <span className="stat-label">{label}</span>
      <strong className="stat-value">{value}</strong>
      <small className="stat-note">{note}</small>
    </div>
  );
}
