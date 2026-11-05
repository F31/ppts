import type { AnimationPreview, AnimationPreviewCapability } from './api/animation';

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
    // 服务端把这个头填成**渲染诊断码**（focus_target_missing），早期前端按简称
    // 'target_missing' 写：两个都认。否则精确文案这一支在真实响应上永远不命中，
    // 全落进兜底——而用例喂的正是简称，两边一起错，谁也看不见。
    const missing =
      preview.focusSkipReason === 'focus_target_missing' ||
      preview.focusSkipReason === 'target_missing';
    const key =
      preview.focusSkipReason === 'canvas_aspect'
        ? 'editor.animationPreviewFocusSkippedAspect'
        : missing
          ? 'editor.animationPreviewFocusSkippedMissing'
          : 'editor.animationPreviewFocusSkipped';
    out.push({ key, params: { count: preview.focusSkipped } });
  }
  return out;
}

// ─── 预览能力 / 按钮状态 ─────────────────────────────────────────────────────
//
// 抽成纯函数是为了能被 node 门禁直接钉住：这一层判错不会抛异常、也不会报错，
// 只是让用户看到一个「点了必然失败」的按钮，或者看到一句没有原因的「预览失败」。
// 2026-11-05 的真实缺陷正是这样：部署没配渲染服务 → 预览路由未注册 → 404 纯文本
// → 前端错误映射没有 http_404 → 落到默认分支，只剩四个字。

type PreviewTranslator = (key: string, params?: Record<string, string | number>) => string;

export type PreviewCapabilityState = 'checking' | 'ready' | 'error';

export type PreviewButtonInput = {
  capability: Pick<AnimationPreviewCapability, 'configured' | 'reason'> | null;
  capabilityState: PreviewCapabilityState;
  /** 有没有配音时间轴：没有时长就无从编译预览。 */
  hasTimeline: boolean;
  /** 有没有已保存的规格：预览渲的是服务器上那份。 */
  hasSavedSpec: boolean;
  previewing: boolean;
  /** 面板整体锁定（只读 / 正在保存）。 */
  panelDisabled: boolean;
};

export type PreviewButtonState = {
  disabled: boolean;
  /** 空串表示不需要额外提示。 */
  hintKey: string;
};

/** 部署级原因码 → 一句能照着做的文案。按钮置灰与 POST 失败共用同一份映射。 */
export function previewUnconfiguredKey(reason: string): string {
  switch (reason) {
    case 'renderer_not_configured':
      return 'editor.animationPreviewUnconfiguredRenderer';
    case 'assets_not_configured':
      return 'editor.animationPreviewUnconfiguredAssets';
    case 'animation_disabled':
      return 'editor.animationPreviewUnconfiguredDisabled';
    case 'renderer_invalid':
    case 'assets_invalid':
      return 'editor.animationPreviewUnconfiguredInvalid';
    default:
      return 'editor.animationPreviewUnconfiguredOther';
  }
}

/**
 * previewButtonState 是「这个按钮现在能不能点」的唯一判据。
 *
 * 顺序不可换：能力没探测完就先锁住（避免探测回来前点出一个必然 404 的请求）；
 * 未配置排在「没配音」之前——用户该先知道这个部署根本没开预览，
 * 而不是照着提示去重新生成配音，配完了按钮还是不能用。
 */
export function previewButtonState(input: PreviewButtonInput): PreviewButtonState {
  const blocked = (hintKey: string): PreviewButtonState => ({ disabled: true, hintKey });
  if (input.capabilityState === 'checking') return blocked('editor.animationPreviewChecking');
  if (input.capabilityState !== 'ready' || !input.capability) {
    return blocked('editor.animationPreviewCapabilityFailed');
  }
  if (!input.capability.configured) return blocked(previewUnconfiguredKey(input.capability.reason));
  if (input.panelDisabled) return blocked('');
  if (!input.hasTimeline) return blocked('editor.animationPreviewNeedsVoice');
  if (!input.hasSavedSpec) return blocked('editor.animationPreviewNeedsSave');
  if (input.previewing) return blocked('');
  return { disabled: false, hintKey: '' };
}

// ─── 失败原因 ────────────────────────────────────────────────────────────────

/** 只取错误里参与决策的三个字段，纯函数不依赖 ConnectError（node 门禁可直接喂对象）。 */
export type PreviewFailure = { code?: string; message?: string; reason?: string };

export function previewErrorDetail(err: PreviewFailure): string {
  const parts: string[] = [];
  if (err.code) parts.push(`code: ${err.code}`);
  if (err.reason) parts.push(`reason: ${err.reason}`);
  if (err.message) parts.push(err.message);
  return parts.join(' · ');
}

/**
 * previewErrorText 把后端信封翻成一句可行动的话。
 *
 * 与保存失败分开映射：那边的 code 是校验规则（V0–V12），这边是渲染链路的状态。
 * 默认分支必须带上服务端 message——此前所有未登记的 code 都被吞成「预览失败」，
 * 用户既无法自助修复，也无法把有效信息反馈给运维。
 */
export function previewErrorText(err: PreviewFailure, t: PreviewTranslator): string {
  const failed = t('editor.animationPreviewFailed');
  const withMessage = () => (err.message ? `${failed}: ${err.message}` : failed);
  switch (err.code) {
    case 'preview_unavailable':
      return t(previewUnconfiguredKey(err.reason ?? ''));
    case 'http_404':
      return t('editor.animationPreviewEndpointMissing');
    case 'unexpected_content_type':
    case 'preview_empty_body':
      return t('editor.animationPreviewResponseNotVideo');
    case 'preview_timeout':
    case 'http_504':
      return t('editor.animationPreviewTimeout');
    case 'no_animation':
      return t('editor.animationPreviewNoAnimation');
    case 'duration_unknown':
      return t('editor.animationPreviewNeedsVoice');
    case 'timeline_unavailable':
      return t('editor.animationPreviewNeedsTimeline');
    case 'slide_not_in_timeline':
      return t('editor.animationPreviewSlideNotInTimeline');
    case 'page_unavailable':
      return t('editor.animationPreviewPageUnavailable');
    case 'page_image_missing':
      return t('editor.animationPreviewNoImage');
    case 'renderer_unavailable':
    case 'http_502':
    case 'http_503':
      return t('editor.animationPreviewServiceDown');
    case 'playback_failed':
      return t('editor.animationPreviewPlaybackFailed');
    case 'compile_failed':
    case 'preview_failed':
    case 'preview_empty':
    case 'asset_publish_failed':
    case 'invalid_body':
    case 'missing_timeline':
    default:
      return withMessage();
  }
}
