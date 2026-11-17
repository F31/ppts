// LookCrop punch-in：当前 Focus.rect 外扩后 crop-to-fill 铺满舞台。
//
// Remotion PageView 与 DOM 播放器（sharePlayer / StagePlayback）必须调用这一份。
// 各写一套的后果是 V13 科技页「MP4 特写、HTML 整页」，而且两边都不报错。
//
// 该 look **不在页坐标画纱/圈选**。裁切本身就是注视。若要把圈选画进 crop 空间，
// 用 cropLocalRect 把页坐标矩形变到舞台坐标。

import type { Box, FocusSpan } from './types.ts';

export const LOOK_CROP_PAD_PX = 8;
export const LOOK_CROP_PAD_MIN = 4;
export const LOOK_CROP_PAD_MAX = 16;

/** 强度只改外扩，不改 1.12 那种假推近。缺省 / 非正强度按 1。 */
export function lookCropPadPx(intensity?: number): number {
  const k = typeof intensity === 'number' && intensity > 0 ? intensity : 1;
  const pad = Math.round(LOOK_CROP_PAD_PX * k);
  return Math.min(LOOK_CROP_PAD_MAX, Math.max(LOOK_CROP_PAD_MIN, pad));
}

export type LookCropFrame = {
  /** 外扩并夹到画布后的裁切盒（页坐标）。 */
  crop: Box;
  scale: number;
  tx: number;
  ty: number;
};

export function lookCropEnabled(opts: {
  lookCrop?: boolean;
  reduceMotion?: boolean;
  hasShotCrop?: boolean;
  /** 保留给调用方；入场层跟底板进同一套 crop 变换，不再关掉 punch-in。 */
  hasEntranceLayers?: boolean;
}): boolean {
  void opts.hasEntranceLayers;
  return Boolean(opts.lookCrop) && !opts.reduceMotion && !opts.hasShotCrop;
}

/** 外扩 pad 像素并夹到画布。退化盒返回 null。 */
export function padFocusRect(rect: Box, pad: number, canvasW: number, canvasH: number): Box | null {
  if (!(rect.w > 0) || !(rect.h > 0) || canvasW <= 0 || canvasH <= 0) return null;
  const p = pad > 0 ? pad : 0;
  let x = rect.x - p;
  let y = rect.y - p;
  let w = rect.w + p * 2;
  let h = rect.h + p * 2;
  if (x < 0) {
    w += x;
    x = 0;
  }
  if (y < 0) {
    h += y;
    y = 0;
  }
  if (x + w > canvasW) w = canvasW - x;
  if (y + h > canvasH) h = canvasH - y;
  if (!(w > 0) || !(h > 0)) return null;
  return { x, y, w, h };
}

/**
 * 短边铺满、长边 center-crop。倍率 = max(canvas/crop)。
 * 变换原点是页图左上 (0,0)：点 (x,y) → (x*scale+tx, y*scale+ty)。
 */
export function lookCropTransform(crop: Box, canvasW: number, canvasH: number): { scale: number; tx: number; ty: number } {
  if (!(crop.w > 0) || !(crop.h > 0) || canvasW <= 0 || canvasH <= 0) {
    return { scale: 1, tx: 0, ty: 0 };
  }
  const scale = Math.max(canvasW / crop.w, canvasH / crop.h);
  const tx = canvasW / 2 - (crop.x + crop.w / 2) * scale;
  const ty = canvasH / 2 - (crop.y + crop.h / 2) * scale;
  return { scale, tx, ty };
}

/** 把页坐标矩形变到 punch-in 之后的舞台坐标。 */
export function cropLocalRect(rect: Box, crop: Box, canvasW: number, canvasH: number): Box {
  const { scale, tx, ty } = lookCropTransform(crop, canvasW, canvasH);
  return {
    x: rect.x * scale + tx,
    y: rect.y * scale + ty,
    w: rect.w * scale,
    h: rect.h * scale
  };
}

function currentFocusRect(focus: FocusSpan[] | undefined, tMs: number): Box | null {
  if (!focus || focus.length === 0) return null;
  let index = -1;
  for (let i = 0; i < focus.length; i++) {
    if (focus[i].startMs <= tMs) index = i;
  }
  if (index < 0) return null;
  const r = focus[index].rect;
  if (!r || !(r.w > 0) || !(r.h > 0)) return null;
  return r;
}

/** 当前 look 的 punch-in。无可用 rect 时返回 null（退回整页静图）。 */
export function lookCropAt(
  focus: FocusSpan[] | undefined,
  tMs: number,
  canvasW: number,
  canvasH: number,
  padPx?: number
): LookCropFrame | null {
  const rect = currentFocusRect(focus, tMs);
  if (!rect) return null;
  const pad = typeof padPx === 'number' && padPx > 0 ? padPx : LOOK_CROP_PAD_PX;
  const crop = padFocusRect(rect, pad, canvasW, canvasH);
  if (!crop) return null;
  const { scale, tx, ty } = lookCropTransform(crop, canvasW, canvasH);
  return { crop, scale, tx, ty };
}

/** 应用到铺满舞台的页图。stageScaled 时位移乘 --ppts-k（DOM 舞台缩放）。 */
export function lookCropImageStyle(
  frame: LookCropFrame | null,
  stageScaled = false
): { transform: string; transformOrigin: string } {
  if (!frame) return { transform: '', transformOrigin: '' };
  const tx = stageScaled ? `calc(${frame.tx}px * var(--ppts-k, 1))` : `${frame.tx}px`;
  const ty = stageScaled ? `calc(${frame.ty}px * var(--ppts-k, 1))` : `${frame.ty}px`;
  return {
    transform: `translate(${tx}, ${ty}) scale(${frame.scale})`,
    transformOrigin: '0 0'
  };
}
