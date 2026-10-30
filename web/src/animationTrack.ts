// 元素级轨道编辑的纯状态层（M4 第 3 段 / 设计方案 §7.6）。
//
// 仍然是同一取向：规则与算术放这里，用 node 跑真代码钉住；组件只管渲染与事件。
//
// 这里有一条**必须写死的语义**，否则整个交互会变成一个会保存、但不起作用的假功能：
//
//	入场片段的起点是由 trigger 决定的（onEnter = 页首，withSegment:x = 第 x 段开口）。
//	用户把它往后拖之后，如果 trigger 不变，渲染器仍然按 trigger 算起点 —— 界面上
//	色块动了，成片里没动，而且没有任何报错。
//
// 所以拖动/微调派生起点的片段时，必须同时把 trigger 改成 `atTime:<ms>`，
// 并在界面上说明"这一条不再跟随朗读时机"（用户可以再选回去）。

// 显式带 .ts 后缀：本模块被 `node --experimental-strip-types animation-check.mjs`
// 直接加载跑用例（测的就是组件调用的这份代码），node 的 ESM 解析不做无后缀补全。
import { TRIGGER_AT_TIME_PREFIX, TRIGGER_ENTER, TRIGGER_SEGMENT_PREFIX } from './animationState.ts';
import type { AnimationClip, AnimationSpec, AnimationTrack } from './animationState.ts';

// 服务端契约（internal/animation/model.go）：改这些值等于改契约。
export const MIN_CLIP_MS = 120;
export const MAX_CLIP_MS = 3000;

// 键盘微调步长（§7.6：方向键 10ms，Shift+方向键 50ms）。
export const NUDGE_MS = 10;
export const NUDGE_SHIFT_MS = 50;

// 撤销栈深度。设计要求 ≥20 步；取 24 是够用且不会把整份规格复制太多份的折中
// （一步就是一次深拷贝，一份规格最多 40 条 clip）。
export const HISTORY_LIMIT = 24;

/** 入场片段的起点是否由 trigger 推导（因此拖动时必须改写 trigger）。 */
export function startIsDerived(clip: AnimationClip): boolean {
  return clip.trigger === TRIGGER_ENTER || clip.trigger.startsWith(TRIGGER_SEGMENT_PREFIX);
}

/** atTime 触发器里显式写下的毫秒数；不是 atTime 时返回 null。 */
export function explicitStartOf(clip: AnimationClip): number | null {
  if (!clip.trigger.startsWith(TRIGGER_AT_TIME_PREFIX)) return null;
  const raw = clip.trigger.slice(TRIGGER_AT_TIME_PREFIX.length);
  const ms = Number(raw);
  return Number.isFinite(ms) ? ms : null;
}

export function clipEnd(clip: AnimationClip): number {
  return clip.startMs + clip.durationMs;
}

/**
 * clampClip 把一条 clip 夹进合法范围。
 *
 * 三个边界：
 *   - 时长 ∈ [120, 3000]（服务端 V5）；
 *   - 起点 ≥ 0；
 *   - 起点 + 时长 ≤ 页时长（服务端 V3）。**页时长为 0 表示还没配音**，
 *     此时跳过上界——服务端在编辑期也允许"先编排、后配音"。
 */
export function clampClip(clip: AnimationClip, pageDurationMs: number): AnimationClip {
  const durationMs = Math.round(Math.min(MAX_CLIP_MS, Math.max(MIN_CLIP_MS, clip.durationMs)));
  const maxStart = pageDurationMs > 0 ? Math.max(0, pageDurationMs - durationMs) : Number.MAX_SAFE_INTEGER;
  const startMs = Math.round(Math.min(maxStart, Math.max(0, clip.startMs)));
  return { ...clip, startMs, durationMs };
}

/**
 * moveClipTo 把某条 clip 的起点移到指定位置。
 *
 * 派生起点的片段会同时改成 `atTime:<startMs>`——见文件头的说明。
 * 已经是 atTime 的片段只更新数字，不改变"固定时间"这个事实。
 */
export function moveClipTo(
  spec: AnimationSpec,
  elementId: string,
  clipIndex: number,
  startMs: number,
  pageDurationMs: number
): AnimationSpec {
  return mapClip(spec, elementId, clipIndex, (clip) => {
    const next = clampClip({ ...clip, startMs }, pageDurationMs);
    if (startIsDerived(clip)) {
      return { ...next, trigger: TRIGGER_AT_TIME_PREFIX + next.startMs };
    }
    if (explicitStartOf(clip) !== null) {
      return { ...next, trigger: TRIGGER_AT_TIME_PREFIX + next.startMs };
    }
    return next;
  });
}

/**
 * resizeClipTo 改某条 clip 的时长（拖右边缘）。
 *
 * 只改时长，不改起点与 trigger：改时长不影响"什么时候开始"，把它也转成 atTime
 * 会让"只想拉长一点"变成"脱离朗读对齐"，是过于激进的行为改变。
 *
 * 页尾空间不够时**截断时长**而不是把起点往左推：用户拖的是右边缘，
 * 色块却整体跳走，会让人以为拖错了对象（第一版就是这么写的）。
 * 只有连最短时长都放不下时（起点已经在页尾 120ms 内）才回退到 clampClip 挪起点。
 */
export function resizeClipTo(
  spec: AnimationSpec,
  elementId: string,
  clipIndex: number,
  durationMs: number,
  pageDurationMs: number
): AnimationSpec {
  return mapClip(spec, elementId, clipIndex, (clip) => {
    const want = Math.round(Math.min(MAX_CLIP_MS, Math.max(MIN_CLIP_MS, durationMs)));
    if (pageDurationMs > 0) {
      const room = pageDurationMs - clip.startMs;
      if (room >= MIN_CLIP_MS) {
        return { ...clip, durationMs: Math.min(want, room) };
      }
      // 起点已经贴到页尾（只可能来自既有非法数据）：收回到最短时长，
      // 并把起点让到刚好放得下它的位置。保持"贴着页尾"比把 clip 往前扔到页中间合理。
      return { ...clip, durationMs: MIN_CLIP_MS, startMs: Math.max(0, pageDurationMs - MIN_CLIP_MS) };
    }
    return clampClip({ ...clip, durationMs: want }, pageDurationMs);
  });
}

/**
 * nudgeClip 键盘微调。direction 为 +1 / -1，big 表示按住 Shift（50ms）。
 * 走 moveClipTo，因此微调入场片段同样会转成 atTime。
 */
export function nudgeClip(
  spec: AnimationSpec,
  elementId: string,
  clipIndex: number,
  direction: number,
  big: boolean,
  pageDurationMs: number
): AnimationSpec {
  const track = spec.tracks.find((item) => item.elementId === elementId);
  const clip = track?.clips[clipIndex];
  if (!clip) return spec;
  const step = big ? NUDGE_SHIFT_MS : NUDGE_MS;
  const delta = direction >= 0 ? step : -step;
  return moveClipTo(spec, elementId, clipIndex, clip.startMs + delta, pageDurationMs);
}

/**
 * clipGeometry 把 clip 换算成时间轴上的百分比位置。
 *
 * 页时长未知（0）时返回 0 宽度：此时不应该画一条"看起来占满全页"的色块，
 * 那会让用户以为已经排好了。
 */
export function clipGeometry(clip: AnimationClip, pageDurationMs: number): { leftPct: number; widthPct: number } {
  if (pageDurationMs <= 0) return { leftPct: 0, widthPct: 0 };
  const left = Math.max(0, Math.min(1, clip.startMs / pageDurationMs));
  const width = Math.max(0, Math.min(1 - left, clip.durationMs / pageDurationMs));
  return { leftPct: left * 100, widthPct: width * 100 };
}

/** 只保留可拖动的元素级轨道（整页轨道的"起点"是页边界，没有可调空间）。 */
export function editableTracks(tracks: AnimationTrack[]): AnimationTrack[] {
  return tracks.filter((track) => track.kind !== 'page' && track.clips.length > 0);
}

// ─── 撤销 / 重做 ────────────────────────────────────────────────────────────
//
// 栈里存的是**整份 draft 快照**，不是"操作"：这个数据量级（最多 40 条 clip）
// 存快照最省心，也不会出现"重放操作序列两次得到不同结果"。

export type TrackHistory = { past: AnimationSpec[]; future: AnimationSpec[] };

export function emptyHistory(): TrackHistory {
  return { past: [], future: [] };
}

/**
 * recordHistory 在**一次编辑开始前**记下快照。
 *
 * 拖拽必须在 pointerdown 调一次、move 时不要再调：按像素记步会让撤销变成
 * "一次撤 1ms"，用户按二十次才退回原处。
 */
export function recordHistory(history: TrackHistory, previous: AnimationSpec): TrackHistory {
  const past = [...history.past, previous];
  return { past: past.length > HISTORY_LIMIT ? past.slice(past.length - HISTORY_LIMIT) : past, future: [] };
}

export function undoHistory(
  history: TrackHistory,
  current: AnimationSpec
): { history: TrackHistory; spec: AnimationSpec } | null {
  if (history.past.length === 0) return null;
  const previous = history.past[history.past.length - 1];
  return {
    spec: previous,
    history: { past: history.past.slice(0, -1), future: [current, ...history.future].slice(0, HISTORY_LIMIT) }
  };
}

export function redoHistory(
  history: TrackHistory,
  current: AnimationSpec
): { history: TrackHistory; spec: AnimationSpec } | null {
  if (history.future.length === 0) return null;
  const next = history.future[0];
  return {
    spec: next,
    history: { past: [...history.past, current].slice(-HISTORY_LIMIT), future: history.future.slice(1) }
  };
}

function mapClip(
  spec: AnimationSpec,
  elementId: string,
  clipIndex: number,
  fn: (clip: AnimationClip) => AnimationClip
): AnimationSpec {
  return {
    ...spec,
    tracks: spec.tracks.map((track) => {
      if (track.elementId !== elementId) return track;
      if (clipIndex < 0 || clipIndex >= track.clips.length) return track;
      return { ...track, clips: track.clips.map((clip, i) => (i === clipIndex ? fn(clip) : clip)) };
    })
  };
}
