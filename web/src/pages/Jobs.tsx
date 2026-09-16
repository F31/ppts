import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  cancelJob,
  getJobDetail,
  getJobsSummary,
  listJobsPage,
  retryFailedJob,
  watchJobEvents,
  type ClientIdentity,
  type JobDetail,
  type JobEventMessage,
  type JobExtras,
  type JobPage,
  type JobScope
} from '../api';
import { describeApiError, settle } from '../apiError';
import { useI18n } from '../i18n';
import { Link, useRoute } from '../router';
import { jobKindKey, jobScopeKindKey, jobStateKey, jobStepStateKey, jobStepTypeKey, type Job, type JobState } from '../types';

const activeStates: JobState[] = [
  'JOB_STATE_QUEUED',
  'JOB_STATE_RUNNING',
  'JOB_STATE_RETRY_WAIT',
  'JOB_STATE_CANCEL_REQUESTED',
  'JOB_STATE_UNKNOWN_PROVIDER_RESULT'
];

const canCancel: JobState[] = [...activeStates];
const canRetry: JobState[] = ['JOB_STATE_FAILED', 'JOB_STATE_UNKNOWN_PROVIDER_RESULT', 'JOB_STATE_CANCELED'];

const PAGE_SIZES = [10, 20, 30];

export function Jobs({ identity }: { identity: ClientIdentity }) {
  const route = useRoute();
  const { t } = useI18n();
  const selectedJobId = route.query.get('job');
  const [pages, setPages] = useState<JobPage[]>([]);
  const [pageIndex, setPageIndex] = useState(0);
  const [pageSize, setPageSize] = useState(10);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<Job | null>(null);
  const [live, setLive] = useState(false);
  // B4-M6a：范围/阶段（批量 summary）与详情（步骤/traceId）。
  // 失败一律显式报错 + 重试入口，不落成"没有数据"（A26）。
  const [extras, setExtras] = useState<Record<string, JobExtras>>({});
  const [extrasError, setExtrasError] = useState('');
  const [extrasNote, setExtrasNote] = useState('');
  const [detail, setDetail] = useState<JobDetail | null>(null);
  const [detailError, setDetailError] = useState('');
  const [detailLoading, setDetailLoading] = useState(false);

  const currentPage = pages[pageIndex];
  const currentJobs = currentPage?.jobs ?? [];
  const allJobs = useMemo(() => pages.flatMap((page) => page.jobs), [pages]);
  // 依赖用「ID 串」而非数组：currentJobs 在无数据时是每渲染新建的空数组，直接作依赖会反复触发请求。
  const pageJobKey = useMemo(() => currentJobs.map((job) => job.jobId).join(','), [currentJobs]);

  // 加载第一页（初始化 / 切换每页数量时重置分页）。
  const loadFirst = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const page = await listJobsPage(identity, { pageSize });
      setPages([page]);
      setPageIndex(0);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('jobs.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [identity, pageSize, t]);

  useEffect(() => {
    void loadFirst();
  }, [loadFirst]);

  // 重新拉取当前已加载的所有页（保持分页位置），用于轮询与手动刷新。
  const refreshLoaded = useCallback(async () => {
    setError('');
    try {
      const result: JobPage[] = [];
      let cursor = '';
      for (let i = 0; i <= pageIndex; i++) {
        const page = await listJobsPage(identity, { cursor, pageSize });
        result.push(page);
        cursor = page.nextCursor;
        if (!cursor) break;
      }
      setPages(result);
      setPageIndex((current) => Math.min(current, Math.max(result.length - 1, 0)));
    } catch (err) {
      setError(err instanceof Error ? err.message : t('jobs.loadFailed'));
    }
  }, [identity, pageIndex, pageSize, t]);

  // 批量取当前页任务的范围/阶段/步骤计数（单次请求，避免逐任务查询）。
  const loadExtras = useCallback(async () => {
    if (!pageJobKey) {
      setExtras({});
      setExtrasError('');
      setExtrasNote('');
      return;
    }
    const { data, error: loadError } = await settle(() => getJobsSummary(identity, pageJobKey.split(',')));
    if (!data) {
      // 三个状态必须可区分：加载失败（本分支）/ 范围缺失（单元格显示 —）/ 步骤读取失败（extrasNote）。
      setExtras({});
      setExtrasError(describeApiError(loadError, t('jobs.extrasLoadFailed'), t));
      setExtrasNote('');
      return;
    }
    setExtrasError('');
    setExtras(data.jobs ?? {});
    // summary 里 stepsError 表示"步骤聚合不可用"：阶段列会为空，界面须说明原因而不是留白。
    setExtrasNote(data.stepsError ? t('jobs.stepsLoadFailed', { msg: t('err.unavailable') }) : '');
  }, [identity, pageJobKey, t]);

  useEffect(() => {
    void loadExtras();
  }, [loadExtras]);

  // 将 WatchEvents 推送的单个任务更新合并进已加载的分页（按 jobId 定位），未在当前页的任务忽略（下次轮询会补齐）。
  const applyJobUpdate = useCallback((updated: Job) => {
    setPages((prev) =>
      prev.map((page) => {
        const idx = page.jobs.findIndex((j) => j.jobId === updated.jobId);
        if (idx === -1) return page;
        const jobs = page.jobs.slice();
        jobs[idx] = updated;
        return { ...page, jobs };
      })
    );
  }, []);

  // 每 5 秒轮询活跃任务（后台压低频率由文档说明，这里统一 5s）。WatchEvents 流优先，此为断线兜底。
  useEffect(() => {
    if (!allJobs.some((job) => activeStates.includes(job.state))) return;
    const timer = window.setInterval(() => void refreshLoaded(), 5000);
    return () => window.clearInterval(timer);
  }, [allJobs, refreshLoaded]);

  // 接入 WatchEvents 服务端流：按项目维度开流，逐条合并更新；任一项目流失败则按 seq 续接重连（最多 3 次），
  // 仍失败则彻底回退到上面的 5s 轮询（断线回退轮询）。无活跃任务时不持有流。
  // watchKey 仅随「项目集合」变化（与任务数据无关），避免每次流式更新都重开流。
  const watchKey = useMemo(
    () => Array.from(new Set(allJobs.map((j) => j.projectId))).filter(Boolean).sort().join(','),
    [allJobs]
  );
  useEffect(() => {
    const pids = watchKey ? watchKey.split(',') : [];
    if (pids.length === 0) {
      setLive(false);
      return;
    }
    const controllers: AbortController[] = [];
    const lastSeq: Record<string, number> = {};
    for (const pid of pids) {
      const ac = new AbortController();
      controllers.push(ac);
      const open = (afterSeq: number, attempt: number) => {
        if (ac.signal.aborted) return;
        watchJobEvents(
          identity,
          pid,
          afterSeq,
          {
            onEvent: (ev: JobEventMessage) => {
              lastSeq[pid] = ev.seq;
              setLive(true);
              applyJobUpdate(ev.job);
            },
            onError: (err: unknown) => {
              if (ac.signal.aborted) return;
              const code = (err as { code?: string }).code;
              if (code === 'unimplemented') return; // 后端不支持 → 永久回退轮询
              if (attempt >= 3) {
                setLive(false);
                return;
              }
              const delay = Math.min(1000 * 2 ** attempt, 8000);
              window.setTimeout(() => open(lastSeq[pid] ?? afterSeq, attempt + 1), delay);
            }
          },
          ac.signal
        );
      };
      open(0, 0);
    }
    return () => {
      controllers.forEach((c) => c.abort());
      setLive(false);
    };
  }, [watchKey, identity, applyJobUpdate]);

  // 深链接选中任务的详情（可能不在当前页）。
  useEffect(() => {
    if (!selectedJobId) {
      setSelected(null);
      return;
    }
    const found = allJobs.find((job) => job.jobId === selectedJobId);
    if (found) setSelected(found);
  }, [selectedJobId, allJobs]);

  // 详情扩展信息（步骤/traceId/范围）：选中任务时拉取；失败时保留基于 Job 的字段并显式报错（A26）。
  // 用请求序号丢弃过期响应：快速切换任务（或反复点重试）时，先发出的慢响应不得覆盖后发出的结果。
  const detailSeqRef = useRef(0);
  const loadDetail = useCallback(
    async (jobId: string) => {
      const seq = (detailSeqRef.current += 1);
      setDetailLoading(true);
      setDetailError('');
      const { data, error: loadError } = await settle(() => getJobDetail(identity, jobId));
      if (seq !== detailSeqRef.current) return; // 已有更新的请求在途 → 丢弃本次结果
      setDetailLoading(false);
      if (!data) {
        setDetail(null);
        setDetailError(describeApiError(loadError, t('jobs.detailLoadFailed'), t));
        return;
      }
      setDetail(data);
    },
    [identity, t]
  );

  useEffect(() => {
    if (!selectedJobId) {
      detailSeqRef.current += 1; // 取消在途请求的写入权
      setDetail(null);
      setDetailError('');
      setDetailLoading(false);
      return;
    }
    void loadDetail(selectedJobId);
  }, [selectedJobId, loadDetail]);

  const goNext = async () => {
    if (!currentPage?.nextCursor || loading) return;
    const nextIndex = pageIndex + 1;
    if (nextIndex < pages.length) {
      setPageIndex(nextIndex);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const page = await listJobsPage(identity, { cursor: currentPage.nextCursor, pageSize });
      setPages((prev) => [...prev, page]);
      setPageIndex(nextIndex);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('jobs.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  const goPrev = () => {
    if (pageIndex > 0) setPageIndex(pageIndex - 1);
  };

  const onCancel = async (job: Job) => {
    try {
      await cancelJob(identity, job.jobId);
      setError('');
      void refreshLoaded();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('jobs.cancelFailed'));
    }
  };

  const onRetry = async (job: Job) => {
    try {
      await retryFailedJob(identity, job.jobId);
      void refreshLoaded();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('jobs.retryFailed'));
    }
  };

  const activeCount = allJobs.filter((job) => activeStates.includes(job.state)).length;
  const selectedJob = selectedJobId ? (selected ?? allJobs.find((job) => job.jobId === selectedJobId) ?? null) : null;

  // 步骤类型是库内自由文本：未登记的类型显示原文（不做猜测性翻译）。
  const stepTypeLabel = (stepType: string) => (jobStepTypeKey[stepType] ? t(jobStepTypeKey[stepType]) : stepType);

  // 范围摘要：以「性质 + 页数」表达，不把内部对象键或未识别的枚举当正常取值渲染。
  const scopeText = (scope: JobScope | undefined): string => {
    if (!scope) return t('common.none');
    switch (scope.kind) {
      case 'project':
        return t('enum.jobScope.project');
      case 'pages':
      case 'segments':
        return `${t(jobScopeKindKey[scope.kind])} · ${t('jobs.scopePages', { count: scope.pageCount })}`;
      case 'export':
        return scope.format ? t('jobs.scopeExport', { format: scope.format }) : t('enum.jobScope.export');
      default:
        return t('enum.jobScope.unknown');
    }
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('jobs.eyebrow')}</span>
          <h1>{t('jobs.title')}</h1>
          <small className="page-sub">
            {activeCount > 0 ? t('jobs.active', { count: activeCount }) : t('jobs.idle')}
            {live && <span className="live-dot" title={t('jobs.live')} />}
          </small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={() => void refreshLoaded()}>
            {t('common.refresh')}
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}

      {selectedJob ? (
        <JobDetailPanel
          job={selectedJob}
          detail={detail}
          loading={detailLoading}
          loadError={detailError}
          scope={detail?.scope ?? extras[selectedJob.jobId]?.scope}
          stepTypeLabel={stepTypeLabel}
          scopeText={scopeText}
          onRetryLoad={() => void loadDetail(selectedJob.jobId)}
          onCancel={() => void onCancel(selectedJob)}
          onRetry={() => void onRetry(selectedJob)}
          canCancelJob={canCancel.includes(selectedJob.state)}
          canRetryJob={canRetry.includes(selectedJob.state)}
        />
      ) : null}

      <section className="panel">
        <header className="table-head">
          <h2>{t('jobs.listTitle')}</h2>
          <div className="pagination">
            <label className="page-size">
              {t('jobs.pageSize')}
              <select value={pageSize} onChange={(e) => setPageSize(Number(e.target.value))}>
                {PAGE_SIZES.map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </select>
            </label>
            <button type="button" disabled={pageIndex === 0 || loading} onClick={goPrev}>
              {t('jobs.prevPage')}
            </button>
            <span className="page-indicator">{t('jobs.pageOf', { page: pageIndex + 1 })}</span>
            <button type="button" disabled={!currentPage?.nextCursor || loading} onClick={() => void goNext()}>
              {t('jobs.nextPage')}
            </button>
          </div>
        </header>

        {/* 范围/阶段读取失败：显式报错 + 重试，不把失败渲染成空白单元格（A26）。 */}
        {extrasError && (
          <div className="load-failure">
            <p className="form-error">{extrasError}</p>
            <button type="button" onClick={() => void loadExtras()}>
              {t('common.retry')}
            </button>
          </div>
        )}
        {!extrasError && extrasNote && <p className="panel-note">{extrasNote}</p>}

        {loading && currentJobs.length === 0 ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : currentJobs.length === 0 ? (
          <p className="empty-state">{t('jobs.empty')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('jobs.colType')}</th>
                <th>{t('jobs.colScope')}</th>
                <th title={t('jobs.colPhaseHint')}>{t('jobs.colPhase')}</th>
                <th>{t('jobs.colState')}</th>
                <th>{t('jobs.colProgress')}</th>
                <th>{t('jobs.colProject')}</th>
                <th>{t('jobs.colSubmitted')}</th>
                <th className="col-actions">{t('jobs.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {currentJobs.map((job) => {
                const extra = extras[job.jobId];
                return (
                  <tr key={job.jobId} className={job.state === 'JOB_STATE_SUCCEEDED' ? 'row-muted' : ''}>
                    <td>
                      <strong className="nowrap-ellipsis">{jobKindKey[job.kind] ? t(jobKindKey[job.kind]) : job.kind}</strong>
                      <small className="cell-sub block-sub">{job.jobId.slice(0, 12)}</small>
                    </td>
                    <td>
                      {scopeText(extra?.scope)}
                      {extra?.scope && extra.scope.pageCount > extra.scope.affectedPages.length && (
                        <small className="cell-sub block-sub">
                          {t('jobs.pagesTruncated', { count: extra.scope.affectedPages.length, total: extra.scope.pageCount })}
                        </small>
                      )}
                    </td>
                    <td title={t('jobs.colPhaseHint')}>
                      {/* 阶段由后端按「最近更新的步骤」推导；无步骤时为空（显示 —，不伪造阶段）。 */}
                      {extra?.phase ? stepTypeLabel(extra.phase) : t('common.none')}
                    </td>
                    <td>
                      <span className={`state-tag ${job.state.toLowerCase()}`}>{t(jobStateKey[job.state])}</span>
                      {job.attempt > 1 && <small className="cell-sub block-sub">{`${t('jobs.colAttempt')} ${job.attempt}`}</small>}
                      {job.lastError && <small className="cell-sub block-sub">{t('jobs.failureReason', { msg: job.lastError.message })}</small>}
                    </td>
                    <td>{job.progressPercent >= 0 ? `${job.progressPercent}%` : '—'}</td>
                    <td>
                      <Link to={`/projects/${job.projectId}/editor`} className="cell-project">
                        {job.projectId.slice(0, 12)}
                      </Link>
                    </td>
                    <td>{new Date(job.createdAtUnix * 1000).toLocaleString()}</td>
                    <td className="col-actions">
                      <div className="row-actions">
                        <Link to={`/jobs?job=${job.jobId}`} className="button-ghost" title={t('jobs.detail')}>
                          {t('common.details')}
                        </Link>
                        {canCancel.includes(job.state) && (
                          <button type="button" onClick={() => void onCancel(job)} title={t('jobs.cancelJob')}>
                            {t('jobs.cancel')}
                          </button>
                        )}
                        {canRetry.includes(job.state) && (
                          <button type="button" onClick={() => void onRetry(job)} title={t('jobs.retryJob')}>
                            {t('jobs.retry')}
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

// JobDetailPanel 渲染单个任务详情（B4-M6a 扩充：范围/受影响页/输入版本/traceId/执行步骤）。
function JobDetailPanel({
  job,
  detail,
  loading,
  loadError,
  scope,
  stepTypeLabel,
  scopeText,
  onRetryLoad,
  onCancel,
  onRetry,
  canCancelJob,
  canRetryJob
}: {
  job: Job;
  detail: JobDetail | null;
  loading: boolean;
  loadError: string;
  scope: JobScope | undefined;
  stepTypeLabel: (stepType: string) => string;
  scopeText: (scope: JobScope | undefined) => string;
  onRetryLoad: () => void;
  onCancel: () => void;
  onRetry: () => void;
  canCancelJob: boolean;
  canRetryJob: boolean;
}) {
  const { t } = useI18n();
  const pages = scope?.affectedPages ?? [];
  const truncated = !!scope && scope.pageCount > pages.length;
  return (
    <section className="panel job-detail">
      <header className="table-head">
        <div>
          <span className="eyebrow">{t('jobs.detailEyebrow')}</span>
          <h2>{(jobKindKey[job.kind] ? t(jobKindKey[job.kind]) : job.kind) + ' · ' + job.jobId}</h2>
        </div>
        <div className="row-actions">
          <Link to={`/projects/${job.projectId}/editor`} className="button-ghost">
            {t('jobs.backToProject')}
          </Link>
          {canCancelJob && (
            <button type="button" onClick={onCancel}>
              {t('jobs.cancelJob')}
            </button>
          )}
          {canRetryJob && (
            <button type="button" onClick={onRetry}>
              {t('jobs.retryJob')}
            </button>
          )}
        </div>
      </header>

      {loadError && (
        <div className="load-failure">
          <p className="form-error">{loadError}</p>
          <button type="button" onClick={onRetryLoad}>
            {t('common.retry')}
          </button>
        </div>
      )}

      <dl className="detail-grid">
        <div>
          <dt>{t('jobs.fieldState')}</dt>
          <dd>
            <span className={`state-tag ${job.state.toLowerCase()}`}>{t(jobStateKey[job.state])}</span>
          </dd>
        </div>
        <div>
          <dt>{t('jobs.fieldProgress')}</dt>
          <dd>{job.progressPercent >= 0 ? `${job.progressPercent}%` : t('jobs.phaseUnknown')}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldProject')}</dt>
          <dd>{job.projectId}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldScope')}</dt>
          <dd>{loading && !detail ? t('common.loading') : scopeText(scope)}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldPages')}</dt>
          <dd>
            {pages.length === 0
              ? t('common.none')
              : `${pages.join('、')}${truncated ? ` ${t('jobs.pagesTruncated', { count: pages.length, total: scope?.pageCount ?? 0 })}` : ''}`}
          </dd>
        </div>
        <div>
          <dt>{t('jobs.fieldInputRevision')}</dt>
          {/* 快照未记录输入版本时为 0：显示 — 而不是伪造版本号。 */}
          <dd>{scope && scope.inputRevision > 0 ? scope.inputRevision : t('common.none')}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldTrace')}</dt>
          <dd>{detail?.traceId || t('common.none')}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldAttempt')}</dt>
          <dd>{job.attempt}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldCreated')}</dt>
          <dd>{new Date(job.createdAtUnix * 1000).toLocaleString()}</dd>
        </div>
        <div>
          <dt>{t('jobs.fieldUpdated')}</dt>
          <dd>{new Date(job.updatedAtUnix * 1000).toLocaleString()}</dd>
        </div>
        {job.lastError && (
          <div className="full-row">
            <dt>{t('jobs.fieldLastError')}</dt>
            <dd className="error-text">
              ({job.lastError.code}) {job.lastError.message}
              {job.lastError.traceId && <small className="cell-sub block-sub">trace: {job.lastError.traceId}</small>}
            </dd>
          </div>
        )}
        {/* 原始快照折叠保留：详情页以可读摘要为主，原始 JSON 仍可追溯（不再作为主字段直接铺开）。 */}
        {job.inputSnapshot && (
          <div className="full-row">
            <dt>{t('jobs.fieldSnapshotRaw')}</dt>
            <dd>
              <details>
                <summary>{t('common.view')}</summary>
                <pre className="snapshot-raw">{job.inputSnapshot}</pre>
              </details>
            </dd>
          </div>
        )}
      </dl>

      <section className="steps-section">
        <header className="table-head">
          <h2>{t('jobs.stepsTitle')}</h2>
          <small className="page-sub">
            {detail && detail.stepTotal > 0 ? t('jobs.stepsTotal', { total: detail.stepTotal }) : ''}
            {detail?.stepsTruncated
              ? ` · ${t('jobs.stepsTruncated', { count: detail.steps.length, total: detail.stepTotal })}`
              : ''}
          </small>
        </header>

        {loading && !detail ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : detail?.stepsError ? (
          // 步骤不可用：「后端未提供该能力」与「读取失败」必须可区分，且都给重试入口。
          <div className="load-failure load-failure-stack">
            <p className="form-error">
              {detail.stepsError === 'unsupported' ? t('jobs.stepsUnsupported') : t('jobs.stepsLoadFailed', { msg: t('err.unavailable') })}
            </p>
            {detail.stepsError !== 'unsupported' && (
              <button type="button" onClick={onRetryLoad}>
                {t('common.retry')}
              </button>
            )}
          </div>
        ) : !detail || detail.steps.length === 0 ? (
          <p className="empty-state">{t('jobs.stepsEmpty')}</p>
        ) : (
          <div className="steps-scroll">
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t('jobs.stepsColType')}</th>
                  <th>{t('jobs.stepsColState')}</th>
                  <th>{t('jobs.stepsColUpdated')}</th>
                  <th>{t('jobs.stepsColResult')}</th>
                </tr>
              </thead>
              <tbody>
                {detail.steps.map((step, index) => (
                  <tr key={`${step.stepType}-${step.updatedAtUnix}-${index}`}>
                    <td>{stepTypeLabel(step.stepType)}</td>
                    <td>
                      <span className={`state-tag step-${step.state}`}>
                        {jobStepStateKey[step.state] ? t(jobStepStateKey[step.state]) : step.state}
                      </span>
                    </td>
                    <td>{new Date(step.updatedAtUnix * 1000).toLocaleString()}</td>
                    {/* result_ref 是内部对象键，后端只回是否已产出。 */}
                    <td>{step.hasResult ? t('jobs.stepsHasResult') : t('common.none')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </section>
  );
}
