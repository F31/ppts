import { useCallback, useEffect, useState } from 'react';
import {
  getNarration,
  getNarrationDraftCount,
  getNarrationStale,
  getProjectArtifacts,
  getStorageUsage,
  getUsage,
  listGateways,
  listJobs,
  listProjectsPage,
  listReviewQueue,
  retryFailedJob,
  type ClientIdentity,
  type ProjectArtifact
} from '../api';
import { describeApiError, settle, type Translator } from '../apiError';
import { useI18n } from '../i18n';
import { can } from '../permissions';
import { Link } from '../router';
import { jobKindKey, jobStateKey, type Job, type JobState, type Project, type Role } from '../types';

// ENRICH_WINDOW：待办信号 / 可播放状态 / 最近成品只探测最近 N 个项目。
// 服务端没有「首页摘要」聚合接口（V1_6 §419 把"总数、待办数、统计时间"列为可选聚合能力，
// 且明确"不遍历全量分页强算"），因此按 V1_6 §202「未取得聚合接口时展示真实最近列表」降级，
// 并在界面上写明统计口径。
const ENRICH_WINDOW = 6;

// PROJECT_PAGE_SIZE：项目列表单页长度。仅用于判断列表是否被截断，**绝不当作总数**（D0-3 / A04）。
const PROJECT_PAGE_SIZE = 20;

// RECENT_ARTIFACT_LIMIT：首页「最近完成成品」展示条数。
const RECENT_ARTIFACT_LIMIT = 5;

const activeStates: JobState[] = [
  'JOB_STATE_QUEUED',
  'JOB_STATE_RUNNING',
  'JOB_STATE_RETRY_WAIT',
  'JOB_STATE_CANCEL_REQUESTED',
  'JOB_STATE_UNKNOWN_PROVIDER_RESULT'
];

// todoJobStates：待处理事项里的任务态。
// FAILED 需人工重试；RETRY_WAIT 是等待自动重试的"待重试"（V1_6 §6.1-3「失败任务」）。
const todoJobStates: JobState[] = ['JOB_STATE_FAILED', 'JOB_STATE_RETRY_WAIT'];

const artifactFormatKey: Record<ProjectArtifact['format'], string> = {
  mp4: 'artifacts.formatMp4',
  srt: 'artifacts.formatSrt',
  vtt: 'artifacts.formatVtt',
  web_project: 'artifacts.formatWebProject'
};

type TodoItem = {
  key: string;
  text: string;
  to: string;
  action: string;
  severity: 'warn' | 'error';
  // retryJobId：仅「任务失败」项提供就地重试（服务端 JobService.RetryFailed 要求 editor）。
  retryJobId?: string;
};

type Stats = {
  usageSeconds: number | null;
  storageBytes: number | null;
  // gatewaysKnown：模型服务是否真的读到了（非 admin 读不到 ≠ 未配置，不能混为一谈）。
  gatewaysKnown: boolean;
  ttsConfigured: boolean;
  llmConfigured: boolean;
};

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

// probeFailureMessages：富化探测会对多个项目并行发起，整租户级的失败（如无权限）会重复出现，
// 去重后只保留不同的原因，并限制条数，避免把一个 403 刷成六行（A26：失败可见，但不要噪音）。
function probeFailureMessages(failures: unknown[], t: Translator): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const failure of failures) {
    const message = describeApiError(failure, t('home.probeFailed'), t);
    if (seen.has(message)) continue;
    seen.add(message);
    out.push(message);
  }
  return out.slice(0, 3);
}

export function Home({ identity, role, roleReady }: { identity: ClientIdentity; role?: Role; roleReady: boolean }) {
  const { t } = useI18n();
  const [projects, setProjects] = useState<Project[]>([]);
  // projectsTruncated：项目列表还有下一页 → 本页长度不是总数（D0-3 / A04）。
  const [projectsTruncated, setProjectsTruncated] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [voiceReady, setVoiceReady] = useState<Record<string, boolean>>({});
  const [draftCounts, setDraftCounts] = useState<Record<string, number>>({});
  const [staleCounts, setStaleCounts] = useState<Record<string, number>>({});
  const [recentArtifacts, setRecentArtifacts] = useState<{ project: Project; artifact: ProjectArtifact }[]>([]);
  const [queueCount, setQueueCount] = useState<number | null>(null);
  const [enrichedCount, setEnrichedCount] = useState(0);
  // probeErrors：待办/成品探测的失败原因（去重）。空数组表示全部读到了。
  const [probeErrors, setProbeErrors] = useState<string[]>([]);
  // usageMonth / usageFetchedAt：用量周期与取数时刻，用于满足 V1_6 §202
  //「用量必须带周期和单位，存储必须标统计时间」。
  const [usageMonth, setUsageMonth] = useState('');
  const [usageFetchedAt, setUsageFetchedAt] = useState<Date | null>(null);
  const [stats, setStats] = useState<Stats>({
    usageSeconds: null,
    storageBytes: null,
    gatewaysKnown: false,
    ttsConfigured: false,
    llmConfigured: false
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [statsError, setStatsError] = useState('');
  const [actionError, setActionError] = useState('');
  const [retryingJobId, setRetryingJobId] = useState('');

  // 能力判定统一走 permissions.ts（A22）。roleReady=false 时不下发角色受限请求：
  // can(undefined,*) 恒真（对齐服务端 nil-reader 放行），但若角色其实更低，就会"先发请求再收 403"，
  // 正是 A22/A26 要消除的假能力闪现。基础数据（项目/任务）只要求已认证，不等角色。
  const canNarration = roleReady && can(role, 'narration.read');
  const canArtifacts = roleReady && can(role, 'artifact.list');
  const canQueue = roleReady && can(role, 'public.manage');
  const canJobControl = roleReady && can(role, 'job.control');
  const canCreateProject = roleReady && can(role, 'project.create');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    setStatsError('');
    setActionError('');
    try {
      const [projectPage, jobList] = await Promise.all([
        listProjectsPage(identity, { pageSize: PROJECT_PAGE_SIZE }),
        listJobs(identity)
      ]);
      const projectList = projectPage.projects;
      setProjects(projectList);
      // nextCursor 非空 = 列表被截断，此时 projects.length 只是本页长度，不是总数（D0-3）。
      setProjectsTruncated(projectPage.nextCursor !== '');
      setJobs(jobList);

      // ---- 用量 / 存储 / 模型服务 ----
      // 显式传 UTC 自然月：后端 monthRange 缺省也是 UTC 当前月（internal/usage/postgres.go:271），
      // 显式传入才能保证界面展示的周期与实际查询周期一致。
      const month = new Date().toISOString().slice(0, 7);
      setUsageMonth(month);
      const [usageRes, storageRes] = await Promise.all([
        settle(() => getUsage(identity, month)),
        settle(() => getStorageUsage(identity))
      ]);
      setUsageFetchedAt(new Date());
      let tts = false;
      let llm = false;
      let gatewaysKnown = false;
      try {
        const gateways = await listGateways(identity);
        tts = gateways.some((g) => g.kind === 'tts' && g.enabled);
        llm = gateways.some((g) => g.kind === 'llm' && g.enabled);
        gatewaysKnown = true;
      } catch (err) {
        // 模型服务读取要求 admin（gateway.go:85）。读不到时不能显示"未配置"——那是把"无权限/失败"
        // 伪装成"确实没配"，卡片改显「—」并在提示行给出原因。
        setStatsError((current) => current || describeApiError(err, t('home.statsFailed'), t));
      }
      setStats({
        usageSeconds: usageRes.data?.secondsUsed ?? null,
        storageBytes: storageRes.data?.totalBytes ?? null,
        gatewaysKnown,
        ttsConfigured: tts,
        llmConfigured: llm
      });
      if (usageRes.error || storageRes.error) {
        setStatsError(describeApiError(usageRes.error ?? storageRes.error, t('home.statsFailed'), t));
      }

      // ---- 待办信号 / 可播放状态 / 最近成品（需 editor 及以上的能力）----
      const window = projectList.slice(0, ENRICH_WINDOW);
      setEnrichedCount(window.length);
      const readyMap: Record<string, boolean> = {};
      const draftMap: Record<string, number> = {};
      const staleMap: Record<string, number> = {};
      const artifactPairs: { project: Project; artifact: ProjectArtifact }[] = [];
      const failures: unknown[] = [];

      await Promise.all(
        window.map(async (project) => {
          if (canNarration) {
            const [narration, drafts, stale] = await Promise.all([
              settle(() => getNarration(identity, project.id)),
              settle(() => getNarrationDraftCount(identity, project.id)),
              settle(() => getNarrationStale(identity, project.id))
            ]);
            // GetNarration 在"尚未生成"时返回 ready=false（不是 404），因此这里任何错误都是真故障。
            if (narration.error) failures.push(narration.error);
            else readyMap[project.id] = narration.data?.ready ?? false;
            if (drafts.error) failures.push(drafts.error);
            else draftMap[project.id] = drafts.data?.draftSegments ?? 0;
            if (stale.error) failures.push(stale.error);
            else staleMap[project.id] = (stale.data?.slides ?? []).filter((slide) => slide.stale).length;
          }
          if (canArtifacts) {
            const res = await settle(() => getProjectArtifacts(identity, project.id));
            if (res.error) failures.push(res.error);
            else (res.data?.artifacts ?? []).forEach((artifact) => artifactPairs.push({ project, artifact }));
          }
        })
      );

      setVoiceReady(readyMap);
      setDraftCounts(draftMap);
      setStaleCounts(staleMap);
      artifactPairs.sort((a, b) => Date.parse(b.artifact.createdAt) - Date.parse(a.artifact.createdAt));
      setRecentArtifacts(artifactPairs.slice(0, RECENT_ARTIFACT_LIMIT));

      // 待审核作品（admin）。后端 publicListMine/publicReviewQueue 不分页且无 LIMIT
      //（internal/public/store.go:150,171），因此 items.length 即精确条数。
      if (canQueue) {
        const res = await settle(() => listReviewQueue(identity, {}));
        if (res.error) failures.push(res.error);
        else setQueueCount((res.data?.items ?? []).length);
      } else {
        setQueueCount(null);
      }

      setProbeErrors(probeFailureMessages(failures, t));
    } catch (err) {
      // A26：主数据（项目/任务）失败必须显式报错，否则页面会用"暂无项目"掩盖真实故障。
      setError(describeApiError(err, t('home.loadFailed'), t));
    } finally {
      setLoading(false);
    }
  }, [identity, t, canNarration, canArtifacts, canQueue]);

  useEffect(() => {
    void load();
  }, [load]);

  const retryJob = useCallback(
    async (jobId: string) => {
      setActionError('');
      setRetryingJobId(jobId);
      const res = await settle(() => retryFailedJob(identity, jobId));
      setRetryingJobId('');
      if (res.error) {
        setActionError(describeApiError(res.error, t('home.retryJobFailed'), t));
        return;
      }
      await load();
    },
    [identity, t, load]
  );

  const projectTitleById = new Map(projects.map((project) => [project.id, project.title]));

  // ---- 待处理事项（全部来自真实接口；无数据则不渲染该区块）----
  const todoItems: TodoItem[] = [];
  if (canNarration) {
    projects.slice(0, ENRICH_WINDOW).forEach((project) => {
      const drafts = draftCounts[project.id] ?? 0;
      if (drafts > 0) {
        todoItems.push({
          key: `draft-${project.id}`,
          text: t('home.todoDraftItem', { title: project.title, count: drafts }),
          to: `/projects/${project.id}/editor`,
          action: t('home.todoGoConfirm'),
          severity: 'warn'
        });
      }
      const stale = staleCounts[project.id] ?? 0;
      if (stale > 0) {
        todoItems.push({
          key: `stale-${project.id}`,
          text: t('home.todoStaleItem', { title: project.title, count: stale }),
          to: `/projects/${project.id}/editor`,
          action: t('home.todoGoRegenerate'),
          severity: 'warn'
        });
      }
    });
  }
  jobs
    .filter((job) => todoJobStates.includes(job.state))
    .forEach((job) => {
      const kind = jobKindKey[job.kind] ? t(jobKindKey[job.kind]) : job.kind;
      todoItems.push({
        key: `job-${job.jobId}`,
        text: t('home.todoJobItem', {
          kind,
          state: t(jobStateKey[job.state]),
          title: projectTitleById.get(job.projectId) ?? t('home.unknownProject')
        }),
        to: `/jobs?job=${job.jobId}`,
        action: t('home.todoGoJob'),
        severity: job.state === 'JOB_STATE_FAILED' ? 'error' : 'warn',
        retryJobId: job.state === 'JOB_STATE_FAILED' && canJobControl ? job.jobId : undefined
      });
    });
  if (queueCount !== null && queueCount > 0) {
    todoItems.push({
      key: 'queue',
      text: t('home.todoQueueItem', { count: queueCount }),
      to: '/settings/public',
      action: t('home.todoGoQueue'),
      severity: 'warn'
    });
  }

  const activeJobCount = jobs.filter((job) => activeStates.includes(job.state)).length;
  const voicedCount = Object.values(voiceReady).filter(Boolean).length;
  // D0-4：可播放数只在最近 ENRICH_WINDOW 个项目上探测过，标签与提示必须写明这一点，
  // 不能让用户把它读成"全租户可播放项目数"。
  const voiceScope = Math.min(enrichedCount, projects.length);
  const fmtSeconds = (seconds: number) =>
    seconds < 60 ? t('home.seconds', { n: seconds }) : t('home.minutes', { n: Math.round((seconds / 60) * 10) / 10 });
  const fmtTime = (value: Date) => value.toLocaleTimeString();

  return (
    <div className="page-stack">
      <section className="home-hero">
        <div>
          <span className="eyebrow">{t('home.eyebrow')}</span>
          <h1>{t('home.welcome', { name: identity.account ?? identity.userId ?? t('home.user') })}</h1>
          <p>{t('home.subtitle')}</p>
        </div>
        {/* 服务端 ProjectService.Create 要求 editor（project.go:35），无权限时不渲染假入口（A22）。 */}
        {canCreateProject && (
          <Link to="/projects" className="button-primary home-cta">
            {t('home.newNarration')}
          </Link>
        )}
      </section>

      {error && (
        <div className="load-failure" role="alert">
          <p className="form-error">{error}</p>
          <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
        </div>
      )}
      {actionError && (
        <div className="load-failure" role="alert">
          <p className="form-error">{actionError}</p>
        </div>
      )}

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
          </div>
        ) : (
          <div className="recent-projects">
            {projects.slice(0, ENRICH_WINDOW).map((project) => (
              <Link key={project.id} to={`/projects/${project.id}/editor`} className="project-card">
                <strong>{project.title}</strong>
                <span>rev {project.currentRevision}</span>
                <em>{voiceReady[project.id] ? t('home.voicedPlayable') : t('home.noVoice')}</em>
              </Link>
            ))}
          </div>
        )}
      </section>

      {/* 待处理事项：V1_6 §6.1-3。没有任何待办时不渲染，避免空壳区块。 */}
      {!loading && !error && todoItems.length > 0 && (
        <section className="panel home-section">
          <header>
            <h2>{t('home.todo')}</h2>
            {/* 作用域说明只在真的按项目探测过时才显示（任务类待办不依赖项目探测窗口）。 */}
            {(canNarration || canArtifacts) && (
              <span className="muted">{t('home.todoScope', { count: voiceScope })}</span>
            )}
          </header>
          <ul className="todo-list">
            {todoItems.map((item) => (
              <li key={item.key} className={`todo-item ${item.severity}`}>
                <span className="todo-text">{item.text}</span>
                {item.retryJobId && (
                  <button
                    type="button"
                    className="button-ghost"
                    disabled={retryingJobId === item.retryJobId}
                    onClick={() => void retryJob(item.retryJobId as string)}
                  >
                    {retryingJobId === item.retryJobId ? t('home.retrying') : t('home.todoRetry')}
                  </button>
                )}
                <Link to={item.to} className="todo-action">{item.action}</Link>
              </li>
            ))}
          </ul>
        </section>
      )}

      {probeErrors.length > 0 && (
        <div className="load-failure load-failure-stack" role="alert">
          {probeErrors.map((message) => (
            <p key={message} className="form-error">{message}</p>
          ))}
          <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
        </div>
      )}

      <section className="stat-grid" aria-label={t('home.statsAria')}>
        {/* D0-3 / A04：ListProjectsResponse 无总数（proto/ppts/v1/project.proto:68）。
            列表完整（无下一页）时才显示精确数；被截断时显示「≥N」并写明"仅最近一页"。 */}
        <StatCard
          label={t('home.stat.projects')}
          value={error ? '—' : projectsTruncated ? t('home.atLeast', { n: projects.length }) : String(projects.length)}
          note={
            error
              ? t('home.stat.unavailable')
              : projectsTruncated
                ? t('home.stat.projectsNotePage', { n: projects.length })
                : t('home.stat.projectsNoteAll')
          }
        />
        {/* D0-4：口径写明"最近 N 项中已配音"，不再冒充全租户可播放项目数。 */}
        <StatCard
          label={t('home.stat.voiced')}
          value={error || !canNarration ? '—' : String(voicedCount)}
          note={
            !canNarration
              ? t('home.stat.needEditor')
              : t('home.stat.voicedNote', { count: voiceScope })
          }
        />
        <StatCard
          label={t('home.stat.activeJobs')}
          value={error ? '—' : String(activeJobCount)}
          note={t('home.stat.activeJobsNote', { count: jobs.length })}
        />
        <StatCard
          label={t('home.stat.usage')}
          value={stats.usageSeconds === null ? '—' : fmtSeconds(stats.usageSeconds)}
          note={
            stats.usageSeconds === null
              ? t('home.stat.unavailable')
              : t('home.stat.usageNote', { month: usageMonth })
          }
        />
        {/* V1_6 §202「存储必须标统计时间」：后端 StorageUsage 未返回统计时间字段，
            因此如实标注为"取数时刻"，不伪造一个服务端统计时间。 */}
        <StatCard
          label={t('home.stat.storage')}
          value={stats.storageBytes === null ? '—' : fmtBytes(stats.storageBytes)}
          note={
            stats.storageBytes === null
              ? t('home.stat.unavailable')
              : usageFetchedAt
                ? t('home.stat.storageNoteAt', { time: fmtTime(usageFetchedAt) })
                : t('home.stat.storageNote')
          }
        />
        <StatCard
          label={t('home.stat.models')}
          value={
            !stats.gatewaysKnown
              ? '—'
              : stats.ttsConfigured && stats.llmConfigured
                ? t('home.models.configured')
                : stats.ttsConfigured || stats.llmConfigured
                  ? t('home.models.partial')
                  : t('home.models.none')
          }
          note={stats.gatewaysKnown ? t('home.stat.modelsNote') : t('home.stat.modelsUnknown')}
        />
      </section>
      {!loading && !error && <p className="stats-note">{t('home.statsNote')}</p>}
      {statsError && <p className="stats-note warn-note">{statsError}</p>}

      {/* 最近完成成品：V1_6 §6.1-5。有成品才渲染，无数据不渲染空壳。 */}
      {!loading && !error && recentArtifacts.length > 0 && (
        <section className="panel home-section">
          <header>
            <h2>{t('home.recentArtifacts')}</h2>
            <span className="muted">{t('home.artifactsScope', { count: voiceScope })}</span>
          </header>
          <div className="artifact-compact">
            {recentArtifacts.map(({ project, artifact }) => (
              <Link
                key={artifact.id}
                to={`/projects/${project.id}/artifacts`}
                className="home-artifact-row"
                title={t('home.artifactOpen', { title: project.title })}
              >
                <span className="home-artifact-format">{t(artifactFormatKey[artifact.format])}</span>
                <strong>{project.title}</strong>
                <span className="home-artifact-size">{fmtBytes(artifact.sizeBytes)}</span>
                <small>{new Date(artifact.createdAt).toLocaleString()}</small>
              </Link>
            ))}
          </div>
        </section>
      )}

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
            {jobs.slice(0, ENRICH_WINDOW).map((job) => (
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
