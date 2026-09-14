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
import { Link } from '../router';
import { jobKindLabel, jobStateLabel, type Job, type JobState, type Project } from '../types';

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

function fmtSeconds(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.round((seconds / 60) * 10) / 10;
  return `${minutes} 分钟`;
}

export function Home({ identity }: { identity: ClientIdentity }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [voiceReady, setVoiceReady] = useState<Record<string, boolean>>({});
  const [stats, setStats] = useState<{ usageSeconds: number; storageBytes: number; ttsConfigured: boolean; llmConfigured: boolean }>({
    usageSeconds: 0,
    storageBytes: 0,
    ttsConfigured: false,
    llmConfigured: false
  });
  const [statsNote, setStatsNote] = useState('');
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
      setStatsNote('统计基于当前已加载数据，未做全量遍历。');
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

  return (
    <div className="page-stack">
      <section className="home-hero">
        <div>
          <span className="eyebrow">控制台首页</span>
          <h1>欢迎回来，{(identity.userId ?? '用户').slice(0, 24)}</h1>
          <p>继续你的讲解创作，或从上传一份新的 PPT 开始。</p>
        </div>
        <div className="home-actions">
          <Link to="/projects" className="button-primary">
            新建讲解
          </Link>
          <Link to="/settings/models" className="button-ghost">
            模型服务
          </Link>
          <Link to="/jobs" className="button-ghost">
            任务中心
          </Link>
        </div>
      </section>

      <section className="stat-grid" aria-label="概览统计">
        <StatCard label="讲解项目" value={String(projects.length)} note="当前已加载" />
        <StatCard label="已配音项目" value={String(voicedCount)} note="数据来源：最近 6 个项目探活" />
        <StatCard label="进行中任务" value={String(activeJobCount)} note="已加载任务范围内" />
        <StatCard label="本月生成时长" value={fmtSeconds(stats.usageSeconds)} note="月用量口径" />
        <StatCard label="存储占用" value={fmtBytes(stats.storageBytes)} note="对象存储汇总" />
        <StatCard label="模型服务" value={stats.ttsConfigured && stats.llmConfigured ? '已配置' : stats.ttsConfigured || stats.llmConfigured ? '部分配置' : '未配置'} note="详见系统设置 · 模型服务" />
      </section>
      {statsNote && <p className="stats-note">{statsNote}</p>}

      <section className="panel home-section">
        <header>
          <h2>最近项目</h2>
          <Link to="/projects" className="link-more">查看全部 →</Link>
        </header>
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : projects.length === 0 ? (
          <div className="empty-state first-run">
            <p>还没有讲解项目。上传第一份 PPT 开始：导入后将自动解析页面，生成讲稿并试听配音。</p>
            <Link to="/projects" className="button-primary">导入第一份 PPT</Link>
          </div>
        ) : (
          <div className="recent-projects">
            {projects.slice(0, 6).map((project) => (
              <Link key={project.id} to={`/projects/${project.id}/editor`} className="project-card">
                <strong>{project.title}</strong>
                <span>rev {project.currentRevision}</span>
                <em>{voiceReady[project.id] ? '已配音，可播放' : '暂无配音'}</em>
              </Link>
            ))}
          </div>
        )}
      </section>

      <section className="panel home-section">
        <header>
          <h2>最近任务</h2>
          <Link to="/jobs" className="link-more">任务中心 →</Link>
        </header>
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : jobs.length === 0 ? (
          <p className="empty-state">暂无任务。</p>
        ) : (
          <div className="jobs-compact">
            {jobs.slice(0, 6).map((job) => (
              <Link key={job.jobId} to={`/jobs?job=${job.jobId}`} className="job-row">
                <span className={`state-tag ${job.state.toLowerCase()}`}>{jobStateLabel[job.state]}</span>
                <strong>{jobKindLabel[job.kind] ?? job.kind}</strong>
                <span className="job-progress">{job.progressPercent >= 0 ? `进度 ${job.progressPercent}%` : ''}</span>
                <small>{new Date(job.updatedAtUnix * 1000).toLocaleString('zh-CN')}</small>
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