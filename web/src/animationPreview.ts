import type { AnimationPreview } from './api/animation';

/** 预览下方的一条说明：i18n 键 + 插值参数。 */
export type PreviewFocusNotice = { key: string; params: Record<string, number> };

/**
 * previewFocusNotices 决定预览下面要说明什么。
 *
 * 单独成纯函数是因为「跳过原因」的文案最容易写错：把 canvas_aspect 说成
 * 「目标找不到」，用户会跑去改讲稿里选的那根柱子——那儿其实没错，错的是
 * 这一页的比例。反过来把未知原因硬塞进某个已知分支，也是在猜。
 */
export function previewFocusNotices(
  preview: Pick<AnimationPreview, 'focus' | 'focusSkipped' | 'focusSkipReason'>
): PreviewFocusNotice[] {
  const out: PreviewFocusNotice[] = [];
  if (preview.focus > 0) {
    out.push({ key: 'editor.animationPreviewFocus', params: { count: preview.focus } });
  }
  if (preview.focusSkipped > 0) {
    // 未知原因不归到已知分支：服务端只给 target_missing / canvas_aspect，
    // 出现别的值说明两边版本不一致，此时给一句不带原因的话比给错原因强。
    const key =
      preview.focusSkipReason === 'canvas_aspect'
        ? 'editor.animationPreviewFocusSkippedAspect'
        : preview.focusSkipReason === 'target_missing'
          ? 'editor.animationPreviewFocusSkippedMissing'
          : 'editor.animationPreviewFocusSkipped';
    out.push({ key, params: { count: preview.focusSkipped } });
  }
  return out;
}
