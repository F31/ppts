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
