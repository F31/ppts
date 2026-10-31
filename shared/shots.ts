// 页内镜头的求值：浏览器播放器与 Remotion 合成器共用这一份。
//
// 为什么单独一个文件而不是各写三行：镜头的判据不是"哪张图"，而是
// **切换窗口的两端**。两端各写一遍「叠化挂在后一镜的开头、进度走 easeOutCubic」，
// 只要有一处把区间写成闭区间，就会出现一帧（30fps 下 33ms）只有一边认为是叠化，
// 于是同一时刻 DOM 与 MP4 各给一张图——看起来像"闪一下"，且无法从任何单端截图发现。

import { easeOutCubic } from './motion.ts';
import type { CameraKeyframe, Shot } from './types.ts';

export type ShotFrame = {
  /** 当前镜头在数组里的位置（不是 shot.index，坏数据也按位置算）。 */
  index: number;
  shot: Shot;
  /** 正在叠出场的上一镜；没有叠化时为 undefined。 */
  from?: Shot;
  /** 1 表示本镜已完全显现；小于 1 表示正在从 `from` 叠化过来（已过缓动）。 */
  progress: number;
};

/**
 * 取页内 `localMs` 时刻该显示哪一镜。
 *
 * 三条约定（与 `openspec/changes/animation-page-shots/design.md` §3 一致）：
 *   1. 镜头区间是 [startMs, startMs+durationMs)；
 *   2. 叠化占**后一镜**的开头 [startMs, startMs+crossFadeMs)，期间两图同时可见；
 *   3. 叠化结束后画面必须已经是纯 B——所以 `progress` 在窗口右端取到 1，
 *      而不是"窗口结束后再淡一下"。
 */
export function shotAt(shots: Shot[] | undefined | null, localMs: number): ShotFrame | null {
  if (!shots || shots.length === 0) return null;

  let index = 0;
  for (let i = 0; i < shots.length; i++) {
    if (shots[i].startMs <= localMs) index = i;
    else break;
  }

  const shot = shots[index];
  const from = index > 0 ? shots[index - 1] : undefined;
  const fade = Math.max(0, shot.crossFadeMs || 0);
  const since = localMs - shot.startMs;
  if (from && fade > 0 && since < fade) {
    return {
      index,
      shot,
      from,
      progress: easeOutCubic(Math.max(0, since) / fade)
    };
  }
  return { index, shot, progress: 1 };
}

/**
 * 在镜内相对时间 `shotLocalMs` 处插值相机变换，返回视口中心 (x, y) 与 scale。
 *
 * 与 Go 侧 render.CameraTransform 逐字节同语义：关键帧按 atMs 升序，早于首帧钳制到首帧、
 * 晚于末帧钳制到末帧；scale 取 max(1, ·) 兜底（scale < 1 会让镜头图盖不住舞台、边缘露白）；
 * x/y 经 clampCenter 钳制到覆盖安全区间 [0.5/scale, 1-0.5/scale]，保证变换后镜头图永远盖住
 * 舞台（即便输入越界，也只裁掉越界部分而非露出空白）。浏览器与合成器共用这一份，两端画面
 * 必须一致——所以这里与 Go 侧都做 clampCenter，不能只在某一端钳制。
 */
export function cameraTransform(kfs: CameraKeyframe[] | undefined | null, shotLocalMs: number): { x: number; y: number; scale: number } {
  if (!kfs || kfs.length === 0) return { x: 0.5, y: 0.5, scale: 1 };
  const lerp = (a: number, b: number, t: number): number => a + (b - a) * t;
  const clampScale = (v: number): number => (v < 1 || Number.isNaN(v) ? 1 : v);
  // 覆盖安全区间钳制（与 Go 侧 clampCenter 同推导）：scale≥1 时只有视口中心落在
  // [0.5/scale, 1-0.5/scale]，缩放后的镜头图才盖得住舞台，否则边缘露白。
  const clampCenter = (x: number, y: number, scale: number): { x: number; y: number; scale: number } => {
    const s = scale <= 0 ? 1 : scale;
    const lo = 0.5 / s;
    const hi = 1 - lo;
    // v != v 是 NaN 判据：NaN 既不小于 lo 也不大于 hi，会原样返回 NaN 从而污染 transform。
    // 与 Go 侧 clampF 一致，把 NaN 当越界钳到 lo，保证输出永远有限。
    const clamp = (v: number): number => (v < lo || Number.isNaN(v) ? lo : v > hi ? hi : v);
    return { x: clamp(x), y: clamp(y), scale: s };
  };
  if (shotLocalMs <= kfs[0].atMs) {
    const k = kfs[0];
    return clampCenter(k.x, k.y, clampScale(k.scale));
  }
  const last = kfs[kfs.length - 1];
  if (shotLocalMs >= last.atMs) {
    return clampCenter(last.x, last.y, clampScale(last.scale));
  }
  for (let i = 1; i < kfs.length; i++) {
    if (shotLocalMs <= kfs[i].atMs) {
      const a = kfs[i - 1];
      const b = kfs[i];
      const t = (shotLocalMs - a.atMs) / (b.atMs - a.atMs);
      return clampCenter(lerp(a.x, b.x, t), lerp(a.y, b.y, t), clampScale(lerp(a.scale, b.scale, t)));
    }
  }
  return clampCenter(last.x, last.y, clampScale(last.scale));
}
