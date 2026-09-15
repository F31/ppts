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
import { describeApiError, settle } from '../apiError';
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
  // A26：用量/存储用 null 表示"取数失败"，界面显示"—"并给出原因；
  // 若沿用 0，会把"取不到数"伪装成"用量为 0"，属于误导性假数据。
  const [stats, setStats] = useState<{
    usageSeconds: number | null;
    storageBytes: number | null;
    ttsConfigured: boolean;
    llmConfigured: boolean;
  }>({
    usageSeconds: null,
    storageBytes: null,
    ttsConfigured: false,
    llmConfigured: false
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [statsError, setStatsError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    setStatsError('');
    try {
      const [projectList, jobList] = await Promise.all([listProjects(identity), listJobs(identity)]);
      setProjects(projectList);
      setJobs(jobList);

      const [usageRes, storageRes] = await Promise.all([
        settle(() => getUsage(identity)),
        settle(() => getStorageUsage(identity))
      ]);
      let tts = false;
      let llm = false;
      try {
        const gateways = await listGateways(identity);
        tts = gateways.some((g) => g.kind === 'tts' && g.enabled);
        llm = gateways.some((g) => g.kind === 'llm' && g.enabled);
      } catch {
        // 非 admin 无法读取模型服务；不阻塞首页，卡片显示"未配置/未知"。
      }
      setStats({
        usageSeconds: usageRes.data?.secondsUsed ?? null,
        storageBytes: storageRes.data?.totalBytes ?? null,
        ttsConfigured: tts,
        llmConfigured: llm
      });
      if (usageRes.error || storageRes.error) {
        setStatsError(describeApiError(usageRes.error ?? storageRes.error, t('home.statsFailed'), t));
      }

      const readyMap: Record<string, boolean> = {};
      const recent = projectList.slice(0, 6);
      await Promise.all(
        recent.map(async (project) => {
          try {
            const narration = await getNarration(identity, project.id);
            readyMap[project.id] = narration.ready;
          } catch {
            // 单项目讲解状态读取失败按"未配音"降级，不影响首页整体渲染。
            readyMap[project.id] = false;
          }
        })
      );
      setVoiceReady(readyMap);
    } catch (err) {
      // A26：主数据（项目/任务）失败必须显式报错，否则页面会用"暂无项目"掩盖真实故障。
      setError(describeApiError(err, t('home.loadFailed'), t));
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

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

      {error && (
        <div className="load-failure" role="alert">
          <p className="form-error">{error}</p>
          <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
        </div>
      )}
      {statsError && !error && <p className="stats-note muted">{statsError}</p>}

      <section className="stat-grid" aria-label={t('home.eyebrow')}>
        <StatCard label={t('home.stat.projects')} value={error ? '—' : String(projects.length)} note={t('home.stat.projectsNote')} />
        <StatCard label={t('home.stat.voiced')} value={error ? '—' : String(voicedCount)} note={t('home.stat.voicedNote')} />
        <StatCard label={t('home.stat.activeJobs')} value={error ? '—' : String(activeJobCount)} note={t('home.stat.activeJobsNote')} />
        <StatCard
          label={t('home.stat.usage')}
          value={stats.usageSeconds === null ? '—' : fmtSeconds(stats.usageSeconds)}
          note={stats.usageSeconds === null ? t('home.stat.unavailable') : t('home.stat.usageNote')}
        />
        <StatCard
          label={t('home.stat.storage')}
          value={stats.storageBytes === null ? '—' : fmtBytes(stats.storageBytes)}
          note={stats.storageBytes === null ? t('home.stat.unavailable') : t('home.stat.storageNote')}
        />
        <StatCard
          label={t('home.stat.models')}
          value={stats.ttsConfigured && stats.llmConfigured ? t('home.models.configured') : stats.ttsConfigured || stats.llmConfigured ? t('home.models.partial') : t('home.models.none')}
          note={t('home.stat.modelsNote')}
        />
      </section>
      {!loading && !error && <p className="stats-note">{t('home.statsNote')}</p>}

      <section className="panel home-section">
        <header>
          <h2>{t('home.recentProjects')}</h2>
          <Link to="/projects" className="link-more">{t('home.viewAll')}</Link>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : error ? (
          <p className="empty-state">{t('home.loadFailedShort')}</p>
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
