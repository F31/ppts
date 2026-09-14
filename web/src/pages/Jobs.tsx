import { useCallback, useEffect, useState } from 'react';
import { cancelJob, listJobs, retryFailedJob, type ClientIdentity } from '../api';
import { Link, useRoute } from '../router';
import { jobKindLabel, jobStateLabel, type Job, type JobState } from '../types';

const activeStates: JobState[] = [
  'JOB_STATE_QUEUED',
  'JOB_STATE_RUNNING',
  'JOB_STATE_RETRY_WAIT',
  'JOB_STATE_CANCEL_REQUESTED',
  'JOB_STATE_UNKNOWN_PROVIDER_RESULT'
];

const canCancel: JobState[] = [...activeStates];
const canRetry: JobState[] = ['JOB_STATE_FAILED', 'JOB_STATE_UNKNOWN_PROVIDER_RESULT', 'JOB_STATE_CANCELED'];

export function Jobs({ identity }: { identity: ClientIdentity }) {
  const route = useRoute();
  const selectedJobId = route.query.get('job');
  const [jobs, setJobs] = useState<Job[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<Job | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const list = await listJobs(identity);
      setJobs(list);
      if (selectedJobId) {
        setSelected(list.find((job) => job.jobId === selectedJobId) ?? null);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '任务列表加载失败');
    } finally {
      setLoading(false);
    }
  }, [identity, selectedJobId]);

  useEffect(() => {
    void load();
  }, [load]);

  // 每 5 秒轮询活跃任务（后台压低频率由文档说明，这里统一 5s）。
  useEffect(() => {
    if (!jobs.some((job) => activeStates.includes(job.state))) return;
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobs]);

  const onCancel = async (job: Job) => {
    try {
      await cancelJob(identity, job.jobId);
      setError('');
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '取消失败');
    }
  };

  const onRetry = async (job: Job) => {
    try {
      await retryFailedJob(identity, job.jobId);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '重试失败');
    }
  };

  const activeCount = jobs.filter((job) => activeStates.includes(job.state)).length;
  const selectedJob = selectedJobId ? (selected ?? jobs.find((job) => job.jobId === selectedJobId) ?? null) : null;

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">任务中心</span>
          <h1>任务</h1>
          <small className="page-sub">
            {activeCount > 0 ? `${activeCount} 个任务进行中，页面自动刷新。` : '当前没有进行中的任务。'}
          </small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={() => void load()}>
            刷新
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
          <h2>任务列表</h2>
        </header>
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : jobs.length === 0 ? (
          <p className="empty-state">暂无任务。上传解析、讲稿生成、配音与导出的任务会出现在这里。</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>类型</th>
                <th>状态</th>
                <th>进度</th>
                <th>项目</th>
                <th>尝试</th>
                <th>提交时间</th>
                <th className="col-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => (
                <tr key={job.jobId} className={job.state === 'JOB_STATE_SUCCEEDED' ? 'row-muted' : ''}>
                  <td>
                    <strong className="nowrap-ellipsis">{jobKindLabel[job.kind] ?? job.kind}</strong>
                    <small className="cell-sub block-sub">{job.jobId.slice(0, 12)}</small>
                  </td>
                  <td>
                    <span className={`state-tag ${job.state.toLowerCase()}`}>{jobStateLabel[job.state]}</span>
                    {job.lastError && <small className="cell-sub block-sub">失败原因：{job.lastError.message}</small>}
                  </td>
                  <td>{job.progressPercent >= 0 ? `${job.progressPercent}%` : '—'}</td>
                  <td>
                    <Link to={`/projects/${job.projectId}/editor`} className="cell-project">
                      {job.projectId.slice(0, 12)}
                    </Link>
                  </td>
                  <td>{job.attempt}</td>
                  <td>{new Date(job.createdAtUnix * 1000).toLocaleString('zh-CN')}</td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <Link to={`/jobs?job=${job.jobId}`} className="button-ghost" title="任务详情">
                        详情
                      </Link>
                      {canCancel.includes(job.state) && (
                        <button type="button" onClick={() => void onCancel(job)} title="取消任务">
                          取消
                        </button>
                      )}
                      {canRetry.includes(job.state) && (
                        <button type="button" onClick={() => void onRetry(job)} title="重试失败任务">
                          重试
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
  return (
    <section className="panel job-detail">
      <header className="table-head">
        <div>
          <span className="eyebrow">任务详情</span>
          <h2>{(jobKindLabel[job.kind] ?? job.kind) + ' · ' + job.jobId}</h2>
        </div>
        <div className="row-actions">
          <Link to={`/projects/${job.projectId}/editor`} className="button-ghost">
            返回项目
          </Link>
          {canCancelJob && (
            <button type="button" onClick={onCancel}>
              取消任务
            </button>
          )}
          {canRetryJob && (
            <button type="button" onClick={onRetry}>
              重试任务
            </button>
          )}
        </div>
      </header>
      <dl className="detail-grid">
        <div>
          <dt>状态</dt>
          <dd>
            <span className={`state-tag ${job.state.toLowerCase()}`}>{jobStateLabel[job.state]}</span>
          </dd>
        </div>
        <div>
          <dt>进度</dt>
          <dd>{job.progressPercent >= 0 ? `${job.progressPercent}%` : '未知（阶段型任务）'}</dd>
        </div>
        <div>
          <dt>项目</dt>
          <dd>{job.projectId}</dd>
        </div>
        <div>
          <dt>输入快照</dt>
          <dd>{job.inputSnapshot || '—'}</dd>
        </div>
        <div>
          <dt>尝试次数</dt>
          <dd>{job.attempt}</dd>
        </div>
        <div>
          <dt>创建时间</dt>
          <dd>{new Date(job.createdAtUnix * 1000).toLocaleString('zh-CN')}</dd>
        </div>
        <div>
          <dt>更新时间</dt>
          <dd>{new Date(job.updatedAtUnix * 1000).toLocaleString('zh-CN')}</dd>
        </div>
        {job.lastError && (
          <div className="full-row">
            <dt>最后错误</dt>
            <dd className="error-text">({job.lastError.code}) {job.lastError.message}</dd>
          </div>
        )}
      </dl>
    </section>
  );
}