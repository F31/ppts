// 时间轴合成器：把 CompiledTimeline 铺成一条绝对时间轴。
//
// 三条不变式（与 IR 规范一一对应）：
//   1. 页的时间由 durationMs 决定，帧边界由 round 决定；页之间首尾相接，不重叠。
//   2. 转场挂在「后一页」，只占该页开头 transition.durationMs，不改变任何一页的时长。
//   3. 图层只做 opacity / translate / scale —— 合成器不认 effect 字段。

import React from 'react';
import { AbsoluteFill, Img, Sequence, staticFile, useCurrentFrame, useVideoConfig } from 'remotion';
import { evaluateLayer, framesForDuration } from './motion';
import type { Layer, Page, Timeline } from './types';

const easeOutCubic = (p: number): number => 1 - Math.pow(1 - p, 3);

/** 一页在静态时刻的画面：底板 + 各元素图层。 */
const PageView: React.FC<{
  page: Page;
  width: number;
  height: number;
  tMs: number;
}> = ({ page, width, height, tMs }) => {
  const plate = staticFile(page.plateImage ?? page.baseImage);
  return (
    <AbsoluteFill>
      <Img src={plate} style={{ width, height, objectFit: 'fill' }} />
      {(page.layers ?? []).map((layer: Layer, i: number) => {
        const state = evaluateLayer(layer, tMs);
        return (
          <div
            key={layer.elementId ?? `layer-${i}`}
            style={{
              position: 'absolute',
              left: layer.box.x,
              top: layer.box.y,
              width: layer.box.w,
              height: layer.box.h,
              overflow: 'hidden',
              opacity: state.opacity,
              transform: `translate(${state.tx}px, ${state.ty}px) scale(${state.sx}, ${state.sy})`,
              transformOrigin: 'center center'
            }}
          >
            {/* 有 layer.image 时直出该层位图（backend 已排除其它动画元素）；
                否则退回从整页底图按偏移裁切——嵌套元素会跟着本层出现（ADR-9）。 */}
            {layer.image ? (
              <Img
                src={staticFile(layer.image)}
                style={{ width: layer.box.w, height: layer.box.h, objectFit: 'fill' }}
              />
            ) : (
              <Img
                src={staticFile(page.baseImage)}
                style={{
                  position: 'absolute',
                  left: -layer.box.x,
                  top: -layer.box.y,
                  width,
                  height,
                  objectFit: 'fill'
                }}
              />
            )}
          </div>
        );
      })}
    </AbsoluteFill>
  );
};

/** 一页的可见内容，含它与前一页之间的转场。 */
const PageEntry: React.FC<{
  page: Page;
  prev?: Page;
  width: number;
  height: number;
  fps: number;
}> = ({ page, prev, width, height, fps }) => {
  const frame = useCurrentFrame();
  const tMs = (frame * 1000) / fps;
  const transitionMs = page.transition?.durationMs ?? 0;
  const inTransition = page.transition.type !== 'none' && prev !== undefined && transitionMs > 0 && tMs < transitionMs;

  if (!inTransition) {
    return <PageView page={page} width={width} height={height} tMs={tMs} />;
  }

  const p = easeOutCubic(Math.min(1, tMs / transitionMs));
  let incoming: React.CSSProperties = {};
  let outgoing: React.CSSProperties = { opacity: 1 };

  switch (page.transition.type) {
    case 'fade':
      incoming = { opacity: p };
      outgoing = { opacity: 1 - p };
      break;
    case 'slide':
      incoming = { transform: `translateX(${(1 - p) * width}px)` };
      break;
    case 'zoom':
      incoming = { opacity: p, transform: `scale(${1 + 0.06 * (1 - p)})` };
      break;
    default:
      incoming = { opacity: p };
      outgoing = { opacity: 1 - p };
      break;
  }

  return (
    <AbsoluteFill>
      {/* 前一页取它自己的末帧：入场动画已回到恒等，因此它与底板一致；
          若该页有出场动画（fadeOut），末帧才是正确画面——不能拿底板顶替。 */}
      <AbsoluteFill style={outgoing}>
        <PageView page={prev} width={width} height={height} tMs={prev.durationMs} />
      </AbsoluteFill>
      <AbsoluteFill style={incoming}>
        <PageView page={page} width={width} height={height} tMs={tMs} />
      </AbsoluteFill>
    </AbsoluteFill>
  );
};

/** 每一页的起始帧。 */
export function pageStartFrames(pages: Page[], fps: number): number[] {
  const starts: number[] = [];
  let cursor = 0;
  for (const page of pages) {
    starts.push(cursor);
    cursor += framesForDuration(page.durationMs, fps);
  }
  return starts;
}

export function totalFrames(timeline: Timeline): number {
  const fps = timeline.fps || 30;
  if (!timeline.pages?.length) return 1;
  return timeline.pages.reduce((sum, page) => sum + framesForDuration(page.durationMs, fps), 0);
}

const TimelineComposition: React.FC<Timeline> = (timeline) => {
  const { fps } = useVideoConfig();
  const starts = pageStartFrames(timeline.pages ?? [], fps);
  return (
    <AbsoluteFill style={{ backgroundColor: timeline.background || '#FFFFFF' }}>
      {(timeline.pages ?? []).map((page, i) => (
        <Sequence
          key={page.slideId || `page-${i}`}
          from={starts[i]}
          durationInFrames={framesForDuration(page.durationMs, fps)}
          layout="none"
        >
          <PageEntry page={page} prev={timeline.pages[i - 1]} width={timeline.width} height={timeline.height} fps={fps} />
        </Sequence>
      ))}
    </AbsoluteFill>
  );
};

export default TimelineComposition;
