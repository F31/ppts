// 元素级轨道时间轴（M4 第 3 段 / §7.6）。
//
// 一个能拖的色块，背后有三件事必须做对，否则它就是"看起来能改、实际不生效"的假功能：
//   1. 拖动入场片段要同时把 trigger 改成 atTime（见 animationTrack.ts 的文件头）；
//   2. 一次拖拽只记**一步**撤销（pointerdown 记，move 不记）；
//   3. 夹紧范围与服务端一致（120–3000ms、不越页尾），别让用户拖出一个必然 400 的规格。
//
// 交互：拖动色块改起点、拖右边缘改时长、方向键 ±10ms、Shift+方向键 ±50ms。

import { useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';

import { anchorLabel } from './animationState';
import type { AnimationAnchors, AnimationSpec } from './animationState';
import { clipGeometry, editableTracks, moveClipTo, nudgeClip, resizeClipTo, startIsDerived } from './animationTrack';

type Translate = (key: string, vars?: Record<string, string | number>) => string;

type DragState = {
  elementId: string;
  clipIndex: number;
  mode: 'move' | 'resize';
  pointerId: number;
  startX: number;
  originStart: number;
  originDuration: number;
  /** 拖动开始时量到的轨道宽度：move 期间不再量——量一次就够，也避免布局变动导致跳动。 */
  laneWidth: number;
};

export function AnimationTrackTimeline({
  spec,
  anchors,
  durationMs,
  disabled,
  onBeforeEdit,
  onChange,
  t
}: {
  spec: AnimationSpec;
  anchors: AnimationAnchors | null;
  /** 页时长（毫秒）。0 = 还没配音，此时只能看，不能编排。 */
  durationMs: number;
  disabled: boolean;
  /** 拖拽/键盘微调开始前调用一次，用来压栈。 */
  onBeforeEdit: () => void;
  onChange: (next: AnimationSpec) => void;
  t: Translate;
}) {
  const [drag, setDrag] = useState<DragState | null>(null);
  const tracks = editableTracks(spec.tracks);
  const canDrag = !disabled && durationMs > 0;

  const beginDrag = (event: ReactPointerEvent, elementId: string, clipIndex: number, mode: 'move' | 'resize') => {
    if (!canDrag) return;
    // 每行各量各的：多行等宽时结果相同，但"最后挂上的那个 ref"在行宽不一致时是错的。
    const lane = (event.currentTarget as HTMLElement).closest('.track-timeline-lane');
    const width = lane?.getBoundingClientRect().width ?? 0;
    const track = spec.tracks.find((item) => item.elementId === elementId);
    const clip = track?.clips[clipIndex];
    if (!width || !clip) return;
    event.stopPropagation();
    event.preventDefault();
    onBeforeEdit();
    // 捕获必须落在**色块**上，而不是当前的 handle：指针捕获会把后续 pointermove
    // 全部重定向到捕获元素，捕在 8px 的把手上的话，色块的 onPointerMove 永远收不到事件，
    // 表现是"按住右边缘拖动，色块不动"。
    const clipEl = (event.currentTarget as HTMLElement).closest('.track-clip') as HTMLElement | null;
    clipEl?.setPointerCapture(event.pointerId);
    setDrag({
      elementId,
      clipIndex,
      mode,
      pointerId: event.pointerId,
      startX: event.clientX,
      originStart: clip.startMs,
      originDuration: clip.durationMs,
      laneWidth: width
    });
  };

  const moveDrag = (event: ReactPointerEvent) => {
    if (!drag || event.pointerId !== drag.pointerId) return;
    if (drag.laneWidth <= 0 || durationMs <= 0) return;
    const deltaMs = Math.round(((event.clientX - drag.startX) / drag.laneWidth) * durationMs);
    if (drag.mode === 'move') {
      onChange(moveClipTo(spec, drag.elementId, drag.clipIndex, drag.originStart + deltaMs, durationMs));
      return;
    }
    onChange(resizeClipTo(spec, drag.elementId, drag.clipIndex, drag.originDuration + deltaMs, durationMs));
  };

  const endDrag = (event: ReactPointerEvent) => {
    if (!drag || event.pointerId !== drag.pointerId) return;
    setDrag(null);
  };

  if (tracks.length === 0) {
    return <p className="form-hint">{t('editor.animationTrackEmpty')}</p>;
  }

  return (
    <div className="track-timeline" aria-label={t('editor.animationTrackTimeline')}>
      <div className="track-timeline-ruler" aria-hidden="true">
        <span />
        <div className="track-timeline-scale">
          <span>0s</span>
          <span>{(durationMs / 1000).toFixed(1)}s</span>
        </div>
      </div>
      {tracks.map((track) => (
        <div className="track-timeline-row" key={track.elementId}>
          <span className="track-timeline-label" title={anchorLabel(anchors, track.elementId, track.elementId)}>
            {anchorLabel(anchors, track.elementId, track.elementId)}
          </span>
          <div className="track-timeline-lane">
            {track.clips.map((clip, index) => {
              const geo = clipGeometry(clip, durationMs);
              const startText = (clip.startMs / 1000).toFixed(2);
              const endText = ((clip.startMs + clip.durationMs) / 1000).toFixed(2);
              const derived = startIsDerived(clip);
              const label = t('editor.animationClipRange', { start: startText, end: endText });
              return (
                <div
                  key={clip.id ?? `${track.elementId}-${index}`}
                  className={`track-clip${derived ? ' derived' : ''}${drag?.elementId === track.elementId && drag.clipIndex === index ? ' dragging' : ''}`}
                  style={{ left: `${geo.leftPct}%`, width: `${geo.widthPct}%` }}
                  role="slider"
                  tabIndex={canDrag ? 0 : -1}
                  aria-label={label}
                  aria-valuemin={0}
                  aria-valuemax={Math.max(0, durationMs - clip.durationMs)}
                  aria-valuenow={clip.startMs}
                  aria-valuetext={label}
                  aria-disabled={!canDrag}
                  title={`${label} · ${derived ? t('editor.animationClipDerived') : t('editor.animationClipFixed')}`}
                  onPointerDown={(event) => beginDrag(event, track.elementId, index, 'move')}
                  onPointerMove={moveDrag}
                  onPointerUp={endDrag}
                  onPointerCancel={endDrag}
                  onKeyDown={(event) => {
                    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
                    if (!canDrag) return;
                    event.preventDefault();
                    event.stopPropagation();
                    onBeforeEdit();
                    onChange(nudgeClip(spec, track.elementId, index, event.key === 'ArrowRight' ? 1 : -1, event.shiftKey, durationMs));
                  }}
                >
                  <span
                    className="track-clip-handle"
                    aria-hidden="true"
                    onPointerDown={(event) => beginDrag(event, track.elementId, index, 'resize')}
                  />
                </div>
              );
            })}
          </div>
        </div>
      ))}
      <p className="form-hint">{t('editor.animationTrackHint')}</p>
    </div>
  );
}
