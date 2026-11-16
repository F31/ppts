// 一键成片：讲稿任务的「真实完成」判据（纯函数，供 node:test 门禁直接加载）。
//
// 历史故障（2026-11-05，job dbec0b01）：server 把 script_draft 记为 succeeded，
// 但 job_steps 里 12 页 failed、3 页 degraded、只有 7 页 success。旧流水线只看
// job.state === SUCCEEDED 就继续配音，等于拿半成品往下跑，界面还报「完成」。
// 这里把判据收敛到逐页步骤：失败 / 降级 / 读不到步骤都必须显式拦住，
// 并且「讲稿页数」只能取真实 success 数，不能用请求时的页数冒充。
import type { JobDetail } from './api/jobs';

export type DraftJobOutcome = {
  ok: boolean;
  generated: number;
  failed: number;
  degraded: number;
  skipped: number;
  // 一页新讲稿都没写成，但步骤都是跳过。调用方还有已保存讲稿时可以继续配音；
  // 一张可用讲稿都没有时才应当停下。
  allSkipped: boolean;
  // 空串表示可以往下走；否则是 i18n key，由调用方翻成用户可见错误。
  errorKey: string;
  params: Record<string, string | number>;
};

export type ScriptReusePlan = {
  // 已有可用讲稿、这一轮保持不动的页。
  reuse: string[];
  // 还没有讲稿、需要成稿任务去补的页。覆盖全部时这里是整个范围。
  generate: string[];
  // 没有缺稿的页，不必再排一次成稿任务。
  skipJob: boolean;
};

// planScriptReuse 决定一键成片的讲稿步怎么对待已经生成的稿。
//
// 默认「只填充空白页」：已有讲稿的页直接沿用，只给空白页排队。
// 「覆盖全部」才整份重写。范围为空时也不排队。
export function planScriptReuse(input: {
  scopedSlideIds: string[];
  scriptSlideIds: string[];
  overwriteAll: boolean;
}): ScriptReusePlan {
  const scoped = input.scopedSlideIds.filter((id) => id !== '');
  if (input.overwriteAll) {
    return { reuse: [], generate: scoped, skipJob: scoped.length === 0 };
  }
  const have = new Set(input.scriptSlideIds);
  const reuse = scoped.filter((id) => have.has(id));
  const generate = scoped.filter((id) => !have.has(id));
  return { reuse, generate, skipJob: generate.length === 0 };
}

const reasonLimit = 160;

export function evaluateDraftJob(detail: JobDetail | null | undefined): DraftJobOutcome {
  const counts = detail?.stepCounts ?? {};
  const generated = countOf(counts, 'success');
  const failed = countOf(counts, 'failed');
  const degraded = countOf(counts, 'degraded');
  const skipped = countOf(counts, 'skipped');
  const base = { generated, failed, degraded, skipped, allSkipped: false };

  // 读不到步骤 != 全部成功（A26：失败必须可见且可解释）。
  if (!detail || detail.stepsError) {
    return {
      ...base,
      ok: false,
      errorKey: 'editor.oneClickDraftUnverifiable',
      params: { error: detail?.stepsError || 'missing_job_detail' }
    };
  }
  if (detail.stepTotal === 0 && generated === 0) {
    return {
      ...base,
      ok: false,
      errorKey: 'editor.oneClickDraftUnverifiable',
      params: { error: 'no_steps' }
    };
  }
  // 全部跳过不是失败：常见情况是这些页已有讲稿（仅补空）或没有可写的正文。
  // 有没有旧稿可沿用要由调用方判断；这里不能把「一页新稿都没写」误判成任务损坏。
  if (generated === 0 && failed === 0 && degraded === 0 && skipped > 0) {
    return {
      ...base,
      ok: true,
      allSkipped: true,
      errorKey: '',
      params: { skipped }
    };
  }
  // 连跳过计数都没有（脏数据 / store 没登记步骤）才算真正无法核验。
  if (generated === 0 && failed === 0 && degraded === 0) {
    return {
      ...base,
      ok: false,
      errorKey: 'editor.oneClickDraftUnverifiable',
      params: { error: 'no_generated_pages' }
    };
  }
  if (failed > 0 || degraded > 0) {
    const reason = firstReason(detail);
    return {
      ...base,
      ok: false,
      errorKey: reason ? 'editor.oneClickDraftIncompleteWithReason' : 'editor.oneClickDraftIncomplete',
      params: reason ? { generated, failed, degraded, reason } : { generated, failed, degraded }
    };
  }
  return { ...base, ok: true, errorKey: '', params: {} };
}

function countOf(counts: Record<string, number>, key: string): number {
  const value = counts[key];
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

function firstReason(detail: JobDetail): string {
  const hit = (detail.steps ?? []).find(
    (step) =>
      (step.state === 'failed' || step.state === 'degraded') && (step.reason ?? '').trim() !== ''
  );
  const text = (hit?.reason ?? '').trim();
  if (!text) return '';
  return text.length > reasonLimit ? `${text.slice(0, reasonLimit)}…` : text;
}
