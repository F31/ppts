// 动画能力原因码 → 用户能看懂的一句话。
//
// 两个场景共用同一套映射：
//   1. 导出对话框里勾选框为什么是灰的（导出前探测）；
//   2. 导出已受理、但后端摘掉了动画（导出后降级，见 ExportJobResult.animationDropped）。
// 各写一遍迟早会漂：对话框说「服务不可用」、提示说「未开启」，用户无法判断到底该找谁。
//
// 只按前缀匹配：`renderer_unavailable: <详情>` 的详情里带地址与错误串，
// 对用户没有信息量（还会把内网地址漏到页面上），所以整段丢弃。
type Translate = (key: string, vars?: Record<string, string | number>) => string;

export function animationReasonText(reason: string | undefined, t: Translate): string {
  const code = (reason ?? '').split(':')[0].trim();
  switch (code) {
    case 'animation_not_available':
    case 'animation_disabled_by_deployment':
    case 'animation_disabled_by_tenant':
    case 'animation_not_in_rollout':
      // 对用户是同一句话「这个功能没开通」：是没进灰度、被管理员关掉、还是部署没上，
      // 都是他无法自行解决的事。运维侧的区分在 capabilities.source / reason 里仍然拿得到。
      return t('editor.exportAnimationNotEnabled');
    case 'animation_policy_unreadable':
      // 这一条刻意与「没开通」分开：它是服务侧故障，用户重试或找管理员处理的方式不同。
      return t('editor.exportAnimationServiceDown');
    case 'renderer_not_configured':
      return t('editor.exportAnimationNotConfigured');
    case 'renderer_unavailable':
      return t('editor.exportAnimationServiceDown');
    default:
      return t('editor.exportAnimationUnavailable');
  }
}

// animationDroppedText 把「后端已摘掉动画」这件事说成一句完整的话。
//
// 之所以要带 jobId：用户此刻已经在任务中心等一个 mp4，指到具体任务才不会
// 让他在列表里猜哪一条是这次导出的。
export function animationDroppedText(reason: string, jobId: string, t: Translate): string {
  return t('editor.exportAnimationDropped', { reason: animationReasonText(reason, t), jobId });
}

// durationSpan 把秒数写成当前语言能读的时长：中文「3 分 00 秒」、英文「3m 00s」。
//
// 放前端做而不是直接用后端 message：后端那句话是中文的，英文界面会突然冒中文；
// 而两个数字（limitSeconds / actualSeconds）已经在错误信封里，拼文案不该再猜。
function durationSpan(seconds: number, lang: 'zh' | 'en'): string {
  const total = Math.max(0, Math.round(seconds));
  const minutes = Math.floor(total / 60);
  const rest = String(total % 60).padStart(2, '0');
  if (lang === 'en') return minutes > 0 ? `${minutes}m ${rest}s` : `${total}s`;
  return minutes > 0 ? `${minutes} 分 ${rest} 秒` : `${total} 秒`;
}

// exportFailureText 把导出请求的失败翻成一句用户能看懂的话。
//
// 额度用尽是一条**必须单独解释**的失败：它是 429，但既不是限流（重试没用），
// 也不是"功能没开通"（找管理员开开关也没用）。用通用的 `error.message` 顶上，
// 用户看到的是一句英文的 "animation render quota for 2026-10 is exhausted"，
// 既不知道该怎么办，也不知道要等到什么时候。
//
// 恢复时间取 Retry-After（core.ts 已解析成 retryAfterSeconds）：用相对秒数换算成
// 本地日期，避免让前端去解析业务字段。
//
// 时长超限（422 export_duration_exceeded，ADR-029 ②）同理，但更必须本地化：
// 后端那句「成片时长 6 分 00 秒 超过当前套餐上限 3 分 00 秒」是中文写死的。
// 数字从错误信封的 details 取，文案走 i18n。
export function exportFailureText(error: unknown, t: Translate, lang: 'zh' | 'en'): string {
  const coded = error as {
    code?: string;
    retryAfterSeconds?: number;
    details?: Record<string, unknown>;
  } | null;
  if (coded && coded.code === 'export_duration_exceeded') {
    const details = coded.details ?? {};
    const limit = Number(details.limitSeconds);
    const actual = Number(details.actualSeconds);
    if (Number.isFinite(limit) && Number.isFinite(actual) && limit > 0 && actual > 0) {
      return t('editor.exportDurationExceeded', {
        limit: durationSpan(limit, lang),
        actual: durationSpan(actual, lang)
      });
    }
    // 数字缺失（旧服务端、网关改写了错误体）时仍走专用文案：退回通用「导出失败」
    // 等于把「改小一点就能过」这条唯一有用的信息丢掉。
    return t('editor.exportDurationExceededBare');
  }
  if (coded && coded.code === 'animation_quota_exceeded') {
    const seconds = coded.retryAfterSeconds;
    if (typeof seconds === 'number' && seconds > 0) {
      const reset = new Date(Date.now() + seconds * 1000);
      return t('editor.exportAnimationQuotaReset', { date: reset.toLocaleDateString() });
    }
    return t('editor.exportAnimationQuota');
  }
  if (error instanceof Error && error.message) return error.message;
  return t('editor.exportFailed');
}
