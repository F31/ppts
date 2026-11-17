// 时间轴合成器：把 CompiledTimeline 铺成一条绝对时间轴。
//
// 三条不变式（与 IR 规范一一对应）：
//   1. 页的时间由 durationMs 决定，帧边界由 round 决定；页之间首尾相接，不重叠。
//   2. 转场挂在「后一页」，只占该页开头 transition.durationMs，不改变任何一页的时长。
//   3. 图层只做 opacity / translate / scale / rotate —— 合成器不认 effect 字段。

import React from 'react';
import { AbsoluteFill, Img, Sequence, staticFile, useCurrentFrame, useVideoConfig } from 'remotion';
import {
  CALLOUT_STROKE,
  CALLOUT_STROKE_PX,
  calloutArriveScale,
  calloutLayout,
  easeOutCubic,
  evaluateAnnotation,
  evaluateLayer,
  layerTransformPx,
  focusDim,
  focusMasks,
  focusOpacity,
  framesForDuration,
  isPageHeading,
  originCss
} from '../../shared/motion';
import { lookCropAt, lookCropEnabled, lookCropImageStyle } from '../../shared/lookcrop';
import { shotAt } from '../../shared/shots';
import type { Layer, Page, Timeline } from './types';

/** 一层元素：位置、透明度、位图。heading 在纱之上时 dim 必须为 0。 */
const LayerView: React.FC<{
  layer: Layer;
  index: number;
  page: Page;
  width: number;
  height: number;
  tMs: number;
  dim: number;
}> = ({ layer, index, page, width, height, tMs, dim }) => {
  const state = evaluateLayer(layer, tMs);
  return (
    <div
      key={layer.elementId ?? `layer-${index}`}
      style={{
        position: 'absolute',
        left: layer.box.x,
        top: layer.box.y,
        width: layer.box.w,
        height: layer.box.h,
        overflow: Math.abs(state.rotate) > 1e-6 ? 'visible' : 'hidden',
        opacity: state.opacity * focusOpacity(dim),
        transform: layerTransformPx(state),
        transformOrigin: originCss(layer.origin)
      }}
    >
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
};

function headingLayerCovers(
  layers: Layer[],
  rect: { x: number; y: number; w: number; h: number },
  width: number,
  height: number
): boolean {
  return layers.some((layer) => {
    if (!isPageHeading(layer.kind, layer.box, width, height)) return false;
    const ax2 = layer.box.x + layer.box.w;
    const ay2 = layer.box.y + layer.box.h;
    const bx2 = rect.x + rect.w;
    const by2 = rect.y + rect.h;
    return layer.box.x < bx2 && ax2 > rect.x && layer.box.y < by2 && ay2 > rect.y;
  });
}

/** 一页在静态时刻的画面：底板 + 各元素图层。 */
const PageView: React.FC<{
  page: Page;
  width: number;
  height: number;
  tMs: number;
}> = ({ page, width, height, tMs }) => {
  const plate = staticFile(page.plateImage ?? page.baseImage);
  // 页内镜头：这一毫秒该显示哪一镜。没有 shots 时返回 null，走与引入镜头之前
  // 完全相同的路径（页级 plate/base），所以旧计划逐像素不变。
  const frame = shotAt(page.shots, tMs);
  // 叠化：底层是上一镜，顶层是新镜按进度淡入。进度到 1 时两图相同，
  // 因此「底层此刻换成新镜」不会有可见跳变。
  const fading = frame !== null && frame.from !== undefined && frame.progress < 1;
  const bottom = frame ? (fading && frame.from ? frame.from.image : frame.shot.image) : '';
  const top = fading ? frame?.shot.image : '';
  // 特写镜头里不画层：层的 box 是整页坐标，局部放大后同一套坐标必然错位，
  // 而错位不会报错，只会看起来「元素飘了」（design §4）。
  // LookCrop punch-in 把底板和入场层放进同一套 crop 变换，层跟着底板走。
  const layers = frame?.shot.crop ? [] : page.layers ?? [];
  const punching = lookCropEnabled({
    lookCrop: page.lookCrop,
    hasShotCrop: Boolean(frame?.shot.crop)
  });
  const punch = punching ? lookCropAt(page.focus, tMs, width, height, page.lookCropPadPx) : null;
  const punchStyle = punch ? lookCropImageStyle(punch, false) : null;
  // 页级焦点：非焦点元素整体退后。位置不动，只降不透明度——
  // 版面是烘焙的底图，改布局不是合成器的事。
  const dims = focusDim(page.focus, tMs);
  // 换段窗口里是**两块**（旧的收拢、新的张开），所以这里是一组而不是一块。
  const masks = punch ? [] : focusMasks(page.focus, tMs);
  const skipPageSpace = Boolean(punch) || Boolean(frame?.shot.crop);
  const headingLayers: Layer[] = [];
  const bodyLayers: Layer[] = [];
  for (const layer of layers) {
    if (isPageHeading(layer.kind, layer.box, width, height)) headingLayers.push(layer);
    else bodyLayers.push(layer);
  }
  return (
    <AbsoluteFill style={punch ? { overflow: 'hidden' } : undefined}>
      <AbsoluteFill style={punchStyle ?? undefined}>
        {frame ? (
          <>
            {/* 两张图各自套一层 AbsoluteFill：AbsoluteFill 是 flex 列容器，
                两张 <Img> 直接放进去会**竖着排**，第二张被排到画框外——
                表现是「叠化没生效」，而 props 与图片路径全都对。
                播放器那边是同一个坑，靠 .ppts-slide 的 position:absolute 解决。 */}
            <AbsoluteFill>
              <Img src={staticFile(bottom)} style={{ width, height, objectFit: 'fill' }} />
            </AbsoluteFill>
            {top ? (
              <AbsoluteFill style={{ opacity: frame?.progress ?? 1 }}>
                <Img src={staticFile(top)} style={{ width, height, objectFit: 'fill' }} />
              </AbsoluteFill>
            ) : null}
          </>
        ) : (
          <AbsoluteFill>
            <Img src={plate} style={{ width, height, objectFit: 'fill' }} />
          </AbsoluteFill>
        )}
        {(skipPageSpace ? layers : bodyLayers).map((layer: Layer, i: number) => (
          <LayerView
            key={layer.elementId ?? `layer-${i}`}
            layer={layer}
            index={i}
            page={page}
            width={width}
            height={height}
            tMs={tMs}
            dim={skipPageSpace && isPageHeading(layer.kind, layer.box, width, height)
              ? 0
              : layer.elementId
                ? dims[layer.elementId] ?? 0
                : 0}
          />
        ))}
      </AbsoluteFill>
      {/* 矩形焦点遮罩：盖在**所有层之上**。它压暗的是「画面」而不是某个图层，
          所以必须最后画——画在层下面等于没画。内嵌图表的柱子没有图层可用，
          只能靠这一块矩形告诉观众「此刻讲的是这根」。
          孔用 box-shadow 撑出来（一个 div 就是「透明孔 + 四周无穷大的阴影」），
          blur 让孔边羽化：硬边会在网格线上切出一亮一淡的接缝。 */}
      {!skipPageSpace
        ? masks.map((mask) => (
            <div
              key={mask.index}
              style={{
                position: 'absolute',
                left: mask.rect.x,
                top: mask.rect.y,
                width: mask.rect.w,
                height: mask.rect.h,
                boxShadow: `0 0 0 9999px rgba(255,255,255,${mask.alpha})`,
                filter: 'blur(8px)',
                pointerEvents: 'none'
              }}
            />
          ))
        : null}
      {/* 页眉大标题盖在纱之上：标题始终是原页清晰度，不被白纱压灰。
          已有标题层（fadeIn）走层自己的不透明度；底板里的标题从 baseImage 裁切。 */}
      {!skipPageSpace
        ? headingLayers.map((layer, i) => (
            <LayerView
              key={layer.elementId ?? `heading-${i}`}
              layer={layer}
              index={i}
              page={page}
              width={width}
              height={height}
              tMs={tMs}
              dim={0}
            />
          ))
        : null}
      {!skipPageSpace
        ? (page.clearRects ?? [])
            .filter((rect) => rect.w > 0 && rect.h > 0 && !headingLayerCovers(headingLayers, rect, width, height))
            .map((rect, i) => (
              <div
                key={`clear-${i}`}
                style={{
                  position: 'absolute',
                  left: rect.x,
                  top: rect.y,
                  width: rect.w,
                  height: rect.h,
                  overflow: 'hidden',
                  pointerEvents: 'none'
                }}
              >
                <Img
                  src={staticFile(page.baseImage)}
                  style={{
                    position: 'absolute',
                    left: -rect.x,
                    top: -rect.y,
                    width,
                    height,
                    objectFit: 'fill'
                  }}
                />
              </div>
            ))
        : null}
      {/* 圈选 / 词级下划线盖在纱之上。caption 只走 DOM（K10），导出构图不得挂台上条。 */}
      {!skipPageSpace
        ? (page.annotations ?? []).map((a, i) => {
            const ev = evaluateAnnotation(a, tMs);
            if (!ev?.rect || ev.rect.w <= 0 || ev.rect.h <= 0) return null;
            if (a.kind === 'pointer') {
              return (
                <div
                  key={`pointer-${i}`}
                  style={{
                    position: 'absolute',
                    left: ev.rect.x,
                    top: ev.rect.y,
                    width: ev.rect.w,
                    height: ev.rect.h,
                    background: CALLOUT_STROKE,
                    borderRadius: 2,
                    opacity: ev.opacity,
                    pointerEvents: 'none'
                  }}
                />
              );
            }
            if (a.kind !== 'callout') return null;
            const box = calloutLayout(ev.rect, ev.shape);
            const arrive = calloutArriveScale(a.startMs, tMs);
            return (
              <div
                key={`callout-${i}`}
                style={{
                  position: 'absolute',
                  left: box.x,
                  top: box.y,
                  width: box.w,
                  height: box.h,
                  boxSizing: 'border-box',
                  border: `${CALLOUT_STROKE_PX}px solid ${CALLOUT_STROKE}`,
                  borderRadius: box.radius,
                  opacity: ev.opacity,
                  transform: arrive === 1 ? undefined : `scale(${arrive})`,
                  transformOrigin: 'center center',
                  pointerEvents: 'none'
                }}
              />
            );
          })
        : null}
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
