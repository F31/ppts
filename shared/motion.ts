// 属性插值：IR 里只有「属性 from → to + cubic-bezier」，这里负责把它变成某一时刻的数值。
//
// 与 DOM 端（web/src/sharePlayer）必须同语义：同一个缓动求解器、同一种叠加顺序。
// 两端漂移的典型后果是「浏览器预览里入场是 600ms，导出的 mp4 里是 400ms」——
// 这是客户最容易发现、也最伤信任的一类 bug（设计方案 §20.4）。

import type { CubicBezier, Layer, Motion } from './types';

/** 层在某一时刻的合成状态。 */
export type LayerState = {
  opacity: number;
  tx: number;
  ty: number;
  sx: number;
  sy: number;
};

export const IDENTITY: LayerState = { opacity: 1, tx: 0, ty: 0, sx: 1, sy: 1 };

const NEWTON_ITERATIONS = 8;
const NEWTON_MIN_SLOPE = 0.001;
const SUBDIVISION_PRECISION = 1e-7;
const SUBDIVISION_MAX_ITERATIONS = 12;

function bezierComponent(t: number, p1: number, p2: number): number {
  // B(t) = 3(1-t)^2 t p1 + 3(1-t) t^2 p2 + t^3，端点固定为 0 与 1。
  const mt = 1 - t;
  return 3 * mt * mt * t * p1 + 3 * mt * t * t * p2 + t * t * t;
}

function bezierSlope(t: number, p1: number, p2: number): number {
  const mt = 1 - t;
  return 3 * mt * mt * p1 + 6 * mt * t * (p2 - p1) + 3 * t * t * (1 - p2);
}

/**
 * cubicBezier 求值：给定进度 x∈[0,1]，返回缓动后的进度 y。
 *
 * 与 CSS `cubic-bezier(x1,y1,x2,y2)` 同一定义：先解 x(t)=x，再取 y(t)。
 * 用 Newton-Raphson 加速，斜率过小时退回二分——与浏览器实现同策略，
 * 因此同一条曲线在两端得到同一组采样点。
 */
export function cubicBezier(curve: CubicBezier, x: number): number {
  if (x <= 0) return 0;
  if (x >= 1) return 1;
  const { x1, y1, x2, y2 } = curve;
  let t = x;
  for (let i = 0; i < NEWTON_ITERATIONS; i++) {
    const slope = bezierSlope(t, x1, x2);
    if (Math.abs(slope) < NEWTON_MIN_SLOPE) break;
    const currentX = bezierComponent(t, x1, x2) - x;
    if (Math.abs(currentX) < SUBDIVISION_PRECISION) return bezierComponent(t, y1, y2);
    t -= currentX / slope;
    if (t < 0) t = 0;
    if (t > 1) t = 1;
  }
  // 退化曲线（斜率过小）用二分兜底：宁慢一点，也不要偏。
  let lo = 0;
  let hi = 1;
  t = x;
  for (let i = 0; i < SUBDIVISION_MAX_ITERATIONS; i++) {
    const currentX = bezierComponent(t, x1, x2);
    if (Math.abs(currentX - x) < SUBDIVISION_PRECISION) break;
    if (currentX < x) lo = t;
    else hi = t;
    t = (lo + hi) / 2;
  }
  return bezierComponent(t, y1, y2);
}

/** 单条 clip 在 tMs 时刻的进度：起点前为 0，终点后为 1。 */
export function clipProgress(clip: Motion, tMs: number): number {
  if (clip.durationMs <= 0) return tMs >= clip.startMs ? 1 : 0;
  const raw = (tMs - clip.startMs) / clip.durationMs;
  if (raw <= 0) return 0;
  if (raw >= 1) return 1;
  return cubicBezier(clip.easing, raw);
}

/**
 * evaluateLayer 把一层上的全部 clip 合成为一组变换。
 *
 * 叠加规则（与 DOM 端一致）：
 *   - opacity 相乘（两个淡入叠在一起不会变成「更不透明」）
 *   - translate 相加（同方向叠加就是位移之和）
 *   - scale 相乘（0.9 × 1.1 回到 1）
 * 顺序无关，因此不依赖 clip 的数组顺序——这一点很重要：
 * 规格里 clip 的书写顺序是用户的自由，不该影响像素。
 */
export function evaluateLayer(layer: Layer, tMs: number): LayerState {
  let state: LayerState = { ...IDENTITY };
  for (const clip of layer.clips ?? []) {
    const p = clipProgress(clip, tMs);
    for (const prop of clip.props) {
      const value = prop.from + (prop.to - prop.from) * p;
      switch (prop.name) {
        case 'opacity':
          state.opacity *= value;
          break;
        case 'translateX':
          state.tx += value;
          break;
        case 'translateY':
          state.ty += value;
          break;
        case 'scale':
          state.sx *= value;
          state.sy *= value;
          break;
        case 'scaleX':
          state.sx *= value;
          break;
        case 'scaleY':
          state.sy *= value;
          break;
        default:
          // 未知属性一律忽略：IR 校验已在服务端拒绝过非法属性名，
          // 这里静默忽略是为了让「新增属性但旧合成器」不至于整片失败。
          break;
      }
    }
  }
  return state;
}

/** 帧数：与 Go 侧 round(durationMs × fps / 1000) 同口径，至少 1 帧。 */
export function framesForDuration(durationMs: number, fps: number): number {
  return Math.max(1, Math.round((durationMs * fps) / 1000));
}
