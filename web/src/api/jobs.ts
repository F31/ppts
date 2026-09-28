// 任务：列表 / 详情 / 筛选翻页 / 取消重试 / 事件流
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { ConnectError, connectErrorFrom, requestRaw } from '../core';
import type { Job } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON, getJSON } from './http';
import { identityHeaders } from './identity';
// ---- 任务（JobService） ----

export async function listJobs(identity: ClientIdentity, projectId?: string): Promise<Job[]> {
  const data = await connectJSON<{ jobs?: Job[] }>(identity, '/ppts.v1.JobService/List', {
    projectId: projectId ?? '',
    pageSize: 50
  });
  return data.jobs ?? [];
}

export type JobPage = { jobs: Job[]; nextCursor: string };

export async function listJobsPage(
  identity: ClientIdentity,
  params: { projectId?: string; cursor?: string; pageSize: number }
): Promise<JobPage> {
  const data = await connectJSON<{ jobs?: Job[]; nextCursor?: { value?: string } }>(
    identity,
    '/ppts.v1.JobService/List',
    {
      projectId: params.projectId ?? '',
      pageSize: params.pageSize,
      ...(params.cursor ? { cursor: { value: params.cursor } } : {})
    }
  );
  return { jobs: data.jobs ?? [], nextCursor: data.nextCursor?.value ?? '' };
}

export async function getJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/Get', { jobId });
}

// ---- 任务扩展详情（B4-M6a：原生 HTTP 端点；本环境 protoc 不可用，故不改 proto）----
//
// 后端 internal/api/jobdetail.go：
//   GET /jobs/{jid}/detail  —— traceId + 范围（由 input_snapshot 推导）+ 执行步骤
//   GET /jobs/summary?ids=… —— 批量「范围 / 阶段 / 步骤计数」，供任务列表两列（避免逐任务查询）
// 权限与 JobService.Get/List 同级（viewer 可见，租户隔离），无需额外能力判定。

export type JobStepState = 'pending' | 'success' | 'skipped' | 'failed';

// JobStep 是一步执行记录；resultRef 属内部对象键，后端只回 hasResult。
export type JobStep = {
  stepType: string;
  state: JobStepState;
  updatedAtUnix: number;
  hasResult: boolean;
};

// JobScope 是任务范围摘要：kind 为范围性质，pageCount 为受影响页数；
// pageCount > affectedPages.length 表示页面数组被后端截断（计数仍完整）。
export type JobScope = {
  kind: 'project' | 'pages' | 'segments' | 'export' | 'unknown';
  pageCount: number;
  affectedPages: string[];
  inputRevision: number;
  format?: string;
};

// JobExtras 是列表侧的单任务扩展信息（summary 端点）。
// B4-M6b 收窄：只回范围。阶段改由 /jobs/page 的 jobs[].phase 提供（唯一来源 jobs.phase），
// 步骤计数只在任务详情里展示——原先的 phase/stepTotal/stepCounts 前端从未消费，已一并去掉。
export type JobExtras = {
  scope: JobScope;
};

export type JobDetail = {
  jobId: string;
  kind: string;
  traceId: string;
  scope: JobScope;
  steps: JobStep[];
  stepCounts: Record<string, number>;
  stepTotal: number;
  stepsTruncated: boolean;
  // stepsError：'unsupported'（后端 store 未提供步骤读取能力）| 'load_failed'（读取失败）。
  // 非空时 steps 必为空数组，界面必须显示原因，不得显示成"没有步骤"。
  stepsError?: string;
};

export async function getJobDetail(identity: ClientIdentity, jobId: string): Promise<JobDetail> {
  return getJSON<JobDetail>(identity, `/jobs/${encodeURIComponent(jobId)}/detail`);
}

export async function getJobsSummary(
  identity: ClientIdentity,
  jobIds: string[]
): Promise<{ jobs: Record<string, JobExtras> }> {
  return getJSON<{ jobs: Record<string, JobExtras> }>(
    identity,
    `/jobs/summary?ids=${encodeURIComponent(jobIds.join(','))}`
  );
}

// ---- B4-M6b：任务列表的筛选 / 排序 / 翻页（原生 HTTP 端点）----
// 为什么不复用 listJobsPage（proto JobService.List）：本环境 protoc 不可用，无法为它加
// sort/phase 参数；此端点还改用 (排序键, id) 的 keyset 游标，翻页不受并发插入影响。

export type JobListSort = 'created' | 'updated' | 'phase' | 'pages';

// JobListRow 是列表行：Job 的基本字段 + 阶段（来自 jobs.phase，空串=尚无步骤）。
export type JobListRow = Job & { phase: string };

export type JobsPageResult = {
  jobs: JobListRow[];
  nextCursor: string;
  // phaseCounts：各阶段的任务数，用于筛选下拉展示可选值与数量；读取失败时给出 phaseCountsError。
  phaseCounts?: Record<string, number>;
  phaseCountsError?: string;
};

export async function getJobsPage(
  identity: ClientIdentity,
  params: { phase?: string; sort: JobListSort; desc: boolean; pageSize: number; cursor?: string }
): Promise<JobsPageResult> {
  const query = new URLSearchParams();
  if (params.phase) query.set('phase', params.phase);
  query.set('sort', params.sort);
  query.set('dir', params.desc ? 'desc' : 'asc');
  query.set('limit', String(params.pageSize));
  if (params.cursor) query.set('cursor', params.cursor);
  const data = await getJSON<{
    jobs?: JobListRow[];
    nextCursor?: string;
    phaseCounts?: Record<string, number>;
    phaseCountsError?: string;
  }>(identity, `/jobs/page?${query.toString()}`);
  return {
    jobs: data.jobs ?? [],
    nextCursor: data.nextCursor ?? '',
    phaseCounts: data.phaseCounts,
    phaseCountsError: data.phaseCountsError
  };
}

export async function cancelJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/Cancel', { jobId });
}

export async function retryFailedJob(identity: ClientIdentity, jobId: string): Promise<Job> {
  return connectJSON<Job>(identity, '/ppts.v1.JobService/RetryFailed', { jobId });
}

export type JobEventMessage = { seq: number; job: Job };

// watchJobEvents 接入 WatchEvents 服务端流（Connect 协议 JSON 信封）。
// 解析「1 字节 flag + 4 字节大端长度 + JSON 消息」的信封流，逐条回调 onEvent。
// 任一错误（HTTP 非 2xx、信封解析失败、网络中断）均回调 onError，由调用方决定回退轮询；
// 调用方应传入 AbortSignal 以便在组件卸载时取消。
export function watchJobEvents(
  identity: ClientIdentity,
  projectId: string,
  afterSeq: number,
  handlers: { onEvent: (ev: JobEventMessage) => void; onError?: (err: unknown) => void },
  signal?: AbortSignal
): void {
  const headers: Record<string, string> = {
    'Content-Type': 'application/connect+json',
    Accept: 'application/connect+json',
    ...identityHeaders(identity)
  };
  // SSE 长连接：显式声明无超时且不做内部重试。
  //   - timeoutMs 0：流会长时间保持打开，15s 默认超时会把一条正常工作的流掐断。
  //   - maxAttempts 1：续接重连必须从上一条事件的 seq 开始，core 不知道这个语义，
  //     若这里也重试会基于 afterSeq=原始值重放，产生重复事件；重连策略归调用方（Jobs.tsx）。
  void requestRaw({
    method: 'POST',
    url: '/ppts.v1.JobService/WatchEvents',
    headers,
    body: { projectId, afterSeq },
    signal,
    timeoutMs: 0,
    maxAttempts: 1,
    label: 'WatchEvents'
  })
    .then(async (response) => {
      if (!response.ok || !response.body) {
        // 流式端点不 throw（错误经 onError 回调上抛），但错误对象必须同样走 Connect 契约：
        // 调用方用 isNotFound/isUnimplemented 判定是否值得重连，非 ConnectError 会让判据失效。
        handlers.onError?.(await connectErrorFrom(response, 'WatchEvents'));
        return;
      }
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = new Uint8Array(0);
      const append = (chunk: Uint8Array) => {
        const next = new Uint8Array(buffer.length + chunk.length);
        next.set(buffer);
        next.set(chunk, buffer.length);
        buffer = next;
      };
      const readFrame = (): JobEventMessage | null => {
        if (buffer.length < 5) return null;
        const flag = buffer[0];
        if (flag !== 0x00) {
          // 仅支持未压缩信封（connect-go 对短消息不压缩）；压缩/未知 → 交给调用方回退。
          // 流式协议错误同样走 Connect 契约：它经 onError 跨到调用方，
          // 调用方用 isNotFound/isUnimplemented 决定是否值得重连。
          throw new ConnectError('unknown', 'unexpected streaming envelope flag');
        }
        const len = ((buffer[1] << 24) | (buffer[2] << 16) | (buffer[3] << 8) | buffer[4]) >>> 0;
        if (len < 0 || buffer.length < 5 + len) return null;
        const data = buffer.slice(5, 5 + len);
        buffer = buffer.slice(5 + len);
        const text = decoder.decode(data);
        return JSON.parse(text) as JobEventMessage;
      };
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value) append(value);
        try {
          for (;;) {
            const frame = readFrame();
            if (!frame) break;
            handlers.onEvent(frame);
          }
        } catch (e) {
          handlers.onError?.(e);
          return;
        }
      }
    })
    .catch((err) => {
      if ((err as Error).name === 'AbortError') return;
      handlers.onError?.(err);
    });
}
