import { useCallback, useEffect, useMemo, useState } from 'react';
import { cancelJob, listJobsPage, retryFailedJob, type ClientIdentity, type JobPage } from '../api';
import { useI18n } from '../i18n';
import { Link, useRoute } from '../router';
import { jobKindKey, jobStateKey, type Job, type JobState } from '../types';

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

  const currentPage = pages[pageIndex];
  const currentJobs = currentPage?.jobs ?? [];
  const allJobs = useMemo(() => pages.flatMap((page) => page.jobs), [pages]);

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

  // 每 5 秒轮询活跃任务（后台压低频率由文档说明，这里统一 5s）。
  useEffect(() => {
    if (!allJobs.some((job) => activeStates.includes(job.state))) return;
    const timer = window.setInterval(() => void refreshLoaded(), 5000);
    return () => window.clearInterval(timer);
  }, [allJobs, refreshLoaded]);

  // 深链接选中任务的详情（可能不在当前页）。
  useEffect(() => {
    if (!selectedJobId) {
      setSelected(null);
      return;
    }
    const found = allJobs.find((job) => job.jobId === selectedJobId);
    if (found) setSelected(found);
  }, [selectedJobId, allJobs]);

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

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('jobs.eyebrow')}</span>
          <h1>{t('jobs.title')}</h1>
          <small className="page-sub">
            {activeCount > 0 ? t('jobs.active', { count: activeCount }) : t('jobs.idle')}
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
        <JobDetail
          job={selectedJob}
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
        {loading && currentJobs.length === 0 ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : currentJobs.length === 0 ? (
          <p className="empty-state">{t('jobs.empty')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('jobs.colType')}</th>
                <th>{t('jobs.colState')}</th>
                <th>{t('jobs.colProgress')}</th>
                <th>{t('jobs.colProject')}</th>
                <th>{t('jobs.colAttempt')}</th>
                <th>{t('jobs.colSubmitted')}</th>
                <th className="col-actions">{t('jobs.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {currentJobs.map((job) => (
                <tr key={job.jobId} className={job.state === 'JOB_STATE_SUCCEEDED' ? 'row-muted' : ''}>
                  <td>
                    <strong className="nowrap-ellipsis">{jobKindKey[job.kind] ? t(jobKindKey[job.kind]) : job.kind}</strong>
                    <small className="cell-sub block-sub">{job.jobId.slice(0, 12)}</small>
                  </td>
                  <td>
                    <span className={`state-tag ${job.state.toLowerCase()}`}>{t(jobStateKey[job.state])}</span>
                    {job.lastError && <small className="cell-sub block-sub">{t('jobs.failureReason', { msg: job.lastError.message })}</small>}
                  </td>
                  <td>{job.progressPercent >= 0 ? `${job.progressPercent}%` : '—'}</td>
                  <td>
                    <Link to={`/projects/${job.projectId}/editor`} className="cell-project">
                      {job.projectId.slice(0, 12)}
                    </Link>
                  </td>
                  <td>{job.attempt}</td>
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
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

function JobDetail({
  job,
  onCancel,
  onRetry,
  canCancelJob,
  canRetryJob
}: {
  job: Job;
  onCancel: () => void;
  onRetry: () => void;
  canCancelJob: boolean;
  canRetryJob: boolean;
}) {
  const { t } = useI18n();
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
          <dt>{t('jobs.fieldInput')}</dt>
          <dd>{job.inputSnapshot || '—'}</dd>
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
            <dd className="error-text">({job.lastError.code}) {job.lastError.message}</dd>
          </div>
        )}
      </dl>
    </section>
  );
}
