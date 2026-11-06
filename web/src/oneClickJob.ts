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
  // 空串表示可以往下走；否则是 i18n key，由调用方翻成用户可见错误。
  errorKey: string;
  params: Record<string, string | number>;
};

const reasonLimit = 160;

export function evaluateDraftJob(detail: JobDetail | null | undefined): DraftJobOutcome {
  const counts = detail?.stepCounts ?? {};
  const generated = countOf(counts, 'success');
  const failed = countOf(counts, 'failed');
  const degraded = countOf(counts, 'degraded');
  const skipped = countOf(counts, 'skipped');
  const base = { generated, failed, degraded, skipped };

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
  // 全部跳过：核验结果其实很清楚（不是「无法核验」），必须如实说「一页新讲稿都没生成」
  // 并给出可行动的原因，不能报「完成 0 页」后继续配音。
  if (generated === 0 && failed === 0 && degraded === 0 && skipped > 0) {
    return {
      ...base,
      ok: false,
      errorKey: 'editor.oneClickDraftAllSkipped',
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
