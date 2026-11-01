/**
 * 讲稿来源锚点的置信度分档（G2-1 置信 UI）。
 *
 * 阈值不是拍脑袋定的，来自后端两条通道的硬事实（internal/app/scriptdraft.go）：
 *   - 结构通道 shape_text / notes：写死 1.0
 *   - 视觉通道 visual_*：clamp 到 (0, 0.85]，缺省 0.7
 * 所以 ≥0.99 只可能是「PPT 里真有这段字」；而 0 与缺失表示没标来源
 * （proto3 的 double 零值会被 protojson 省略，前端拿到的是 undefined）。
 *
 * 分三档而不是两档：把「模型看图猜的」和「没标来源」都算作要核对，
 * 但对用户得说清是哪一种——前者可以去看图确认，后者连依据都没有。
 */

/** 结构通道的置信度是 1.0，视觉通道上限 0.85，这里取一个不可能被视觉通道越过的分界。 */
export const TRUSTED_CONFIDENCE = 0.99;

export type AnchorConfidenceLevel = 'trusted' | 'inferred' | 'unverified';

/** 只依赖前端真正拿得到的字段，便于用普通对象构造用例。 */
export type AnchorLike = {
  kind?: string;
  confidence?: number;
};

export function anchorConfidenceLevel(anchor: AnchorLike): AnchorConfidenceLevel {
  const c = anchor.confidence;
  if (typeof c !== 'number' || !Number.isFinite(c) || c <= 0) return 'unverified';
  return c >= TRUSTED_CONFIDENCE ? 'trusted' : 'inferred';
}

/** 除「结构通道」外都要用户核对。 */
export function anchorNeedsReview(anchor: AnchorLike): boolean {
  return anchorConfidenceLevel(anchor) !== 'trusted';
}

export type AnchorSummary = {
  total: number;
  trusted: number;
  needsReview: number;
};

export function summarizeAnchors(anchors: AnchorLike[] | undefined | null): AnchorSummary {
  const list = anchors ?? [];
  let trusted = 0;
  for (const anchor of list) {
    if (anchorConfidenceLevel(anchor) === 'trusted') trusted += 1;
  }
  return { total: list.length, trusted, needsReview: list.length - trusted };
}

/** 只要有一段来源不是结构通道，这段讲稿就该被标出来。 */
export function segmentNeedsReview(segment: { sourceAnchors?: AnchorLike[] }): boolean {
  return (segment.sourceAnchors ?? []).some(anchorNeedsReview);
}

/**
 * 段内最该警惕的那一种待核对来源，用来决定提示文案。
 * 「没标来源」比「模型推断」更糟，所以优先返回它。
 */
export function segmentReviewReason(segment: { sourceAnchors?: AnchorLike[] }): AnchorConfidenceLevel | null {
  const levels = (segment.sourceAnchors ?? []).map(anchorConfidenceLevel);
  if (levels.includes('unverified')) return 'unverified';
  if (levels.includes('inferred')) return 'inferred';
  return null;
}
