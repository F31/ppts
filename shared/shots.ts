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
 * 与 Go 侧 render.CameraTransform 同语义：关键帧按 atMs 升序，早于首帧钳制到首帧、
 * 晚于末帧钳制到末帧，scale 取 max(1, ·) 兜底（scale < 1 会让镜头图盖不住舞台、边缘露白）。
 * 浏览器与合成器共用这一份，保证两端画面一致。
 */
export function cameraTransform(kfs: CameraKeyframe[] | undefined | null, shotLocalMs: number): { x: number; y: number; scale: number } {
  if (!kfs || kfs.length === 0) return { x: 0.5, y: 0.5, scale: 1 };
  const lerp = (a: number, b: number, t: number): number => a + (b - a) * t;
  const clampScale = (v: number): number => (v < 1 || Number.isNaN(v) ? 1 : v);
  if (shotLocalMs <= kfs[0].atMs) {
    const k = kfs[0];
    return { x: k.x, y: k.y, scale: clampScale(k.scale) };
  }
  const last = kfs[kfs.length - 1];
  if (shotLocalMs >= last.atMs) {
    return { x: last.x, y: last.y, scale: clampScale(last.scale) };
  }
  for (let i = 1; i < kfs.length; i++) {
    if (shotLocalMs <= kfs[i].atMs) {
      const a = kfs[i - 1];
      const b = kfs[i];
      const t = (shotLocalMs - a.atMs) / (b.atMs - a.atMs);
      return { x: lerp(a.x, b.x, t), y: lerp(a.y, b.y, t), scale: clampScale(lerp(a.scale, b.scale, t)) };
    }
  }
  return { x: last.x, y: last.y, scale: clampScale(last.scale) };
}
