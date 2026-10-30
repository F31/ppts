// 成品包的动画层：把清单里的 clips 编译成 WAAPI 关键帧，并按音频时钟回放。
//
// 三条边界，都不是"顺手"，而是这个文件的全部理由：
//
// 1. **求值只调一次**：`evaluateLayer` 来自共享模块，与 Remotion 合成器是同一份代码。
//    DOM 端不另写插值——两份求解器的漂移只会在叠加 clip 的页上暴露，
//    而那时已经上线了。
//
// 2. **采样率 = 成片帧率**。MP4 是 30fps，所以 DOM 端只要在每帧时刻拿到同一个值就够了；
//    帧与帧之间用线性插值是**对的**，因为 MP4 在帧之间也没有定义。选更高的采样率
//    只是让包变大，不会让画面更准。
//
// 3. **回放不写 style**。所有关键帧编译成 Animation 对象后，同步只做一件事：
//    设 `currentTime`。transform/opacity 走合成层，逐帧写 style 会触发布局与重绘。
//
// 时钟一律由调用方给（音频 currentTime），这里不持有任何"当前时间"。

import { evaluateLayer, focusDim, focusOpacity, originCss } from '../../../shared/motion.ts';
import type { FocusSpan, Layer, Motion } from '../../../shared/types.ts';

/** 清单里的一层（字段是 IR 的投影，见 internal/share/manifest.go 的 SlideLayer）。 */
export type ManifestLayer = {
  elementId?: string;
  kind?: string;
  box: { x: number; y: number; w: number; h: number };
  image: string;
  clips?: Motion[];
  /** 缩放锚点；缺省 center。柱子的「从基线长上去」靠它。 */
  origin?: string;
};

export type CompileOptions = {
  /** 成片帧率。采样步长取 1000/fps：这是唯一能让 DOM 与 MP4 同帧对齐的步长。 */
  fps: number;
  /** 该页时长（毫秒）。关键帧的 offset 按它归一，保证一条 animation 覆盖整页。 */
  pageDurationMs: number;
  /** prefers-reduced-motion：保留 opacity，去掉位移与缩放，**时长不变**。 */
  reduceMotion: boolean;
  /** 页级焦点：此刻讲的是谁。缺省表示这页没有让位关系。 */
  focus?: FocusSpan[];
};

/** 一页编译结果：每层一个 Animation，按清单顺序。 */
export type CompiledLayer = {
  element: HTMLElement;
  animation: Animation | null;
};

const IDENTITY_TRANSFORM = 'translate(0, 0) scale(1, 1)';

/**
 * 把一层的 clips 采样成 WAAPI keyframes。
 *
 * 采样区间只覆盖 clip 实际活动的时段：前后各插一个"端点值"关键帧即可。
 * 对整页均匀采样会让 6 秒的页产生 180 个关键帧，而其中 170 个的值完全相同。
 */
export function compileKeyframes(
  clips: Motion[],
  opts: CompileOptions,
  elementId?: string
): Keyframe[] | null {
  if (!clips || clips.length === 0) return null;
  let start = Infinity;
  let end = -Infinity;
  for (const clip of clips) {
    if (clip.startMs < start) start = clip.startMs;
    const finish = clip.startMs + Math.max(0, clip.durationMs);
    if (finish > end) end = finish;
  }
  if (!isFinite(start) || !isFinite(end) || end <= start) return null;

  const pageMs = opts.pageDurationMs > 0 ? opts.pageDurationMs : end;
  const fps = opts.fps > 0 ? opts.fps : 30;
  const step = 1000 / fps;

  // 采样点：整页起点、活动区间内每帧一个、整页终点。去重后必须严格递增——
  // offset 重复时 WAAPI 的行为是"后者覆盖前者"，画面会跳一下。
  const times: number[] = [0];
  for (let t = start; t < end; t += step) times.push(t);
  times.push(end);
  // 焦点切换点必须显式采样：它通常落在任何 clip 的活动区间之外（讲完才切换），
  // 只按 clip 区间采样会让播放器从最后一个关键帧直接线性插到页末，
  // 压暗过程被拉长成整页——MP4 里有 240ms 的过渡，网页里变成 8 秒的渐暗。
  for (const f of opts.focus ?? []) {
    times.push(f.startMs);
    times.push(f.startMs + (f.fadeMs ?? 240));
  }
  times.push(pageMs);
  const points: number[] = [];
  for (const t of times) {
    const clamped = Math.max(0, Math.min(pageMs, t));
    if (points.length === 0 || clamped > points[points.length - 1]) points.push(clamped);
  }
  if (points.length < 2) return null;

  const layer: Layer = {
    kind: 'text',
    // assetMode 在这条路径上没有用途（不裁切、不回退），但类型要求它存在。
    assetMode: 'bitmap-region',
    box: { x: 0, y: 0, w: 0, h: 0 },
    clips
  };

  const frames: Keyframe[] = [];
  for (const t of points) {
    const state = evaluateLayer(layer, t);
    const dim = elementId ? focusDim(opts.focus, t)[elementId] ?? 0 : 0;
    const opacity = clamp01(state.opacity * focusOpacity(dim));
    const tx = opts.reduceMotion ? 0 : state.tx;
    const ty = opts.reduceMotion ? 0 : state.ty;
    const sx = opts.reduceMotion ? 1 : state.sx;
    const sy = opts.reduceMotion ? 1 : state.sy;
    frames.push({
      offset: t / pageMs,
      opacity: String(opacity),
      // 位移单位是画布像素，而层是按百分比铺在舞台上的；--ppts-k 是"1 画布像素等于多少
      // 实际像素"，由 resize 时写入。用 calc 而不是预先乘好，是因为窗口尺寸会变。
      transform:
        tx === 0 && ty === 0 && sx === 1 && sy === 1
          ? IDENTITY_TRANSFORM
          : `translate(calc(${round(tx)}px * var(--ppts-k, 1)), calc(${round(ty)}px * var(--ppts-k, 1))) ` +
            `scale(${round(sx)}, ${round(sy)})`
    });
  }
  return frames;
}

/**
 * 为舞台建一页的图层 DOM 与 Animation。
 *
 * 元素尺寸与位置全部用百分比：清单里的 box 是画布像素，舞台按同一宽高比渲染，
 * 所以 x/W 与 w/W 直接就是百分比。用 px 会让 4:3 的页在 16:9 的舞台上错位。
 */
export function buildLayerNodes(
  container: HTMLElement,
  layers: ManifestLayer[],
  canvasW: number,
  canvasH: number,
  opts: CompileOptions
): CompiledLayer[] {
  const out: CompiledLayer[] = [];
  const canAnimate = typeof container.animate === 'function';
  for (const layer of layers) {
    if (!layer.image || layer.box.w <= 0 || layer.box.h <= 0) continue;
    const node = document.createElement('div');
    node.className = 'ppts-layer';
    node.style.left = pct(layer.box.x, canvasW);
    node.style.top = pct(layer.box.y, canvasH);
    node.style.width = pct(layer.box.w, canvasW);
    node.style.height = pct(layer.box.h, canvasH);
    // 锚点写进内联样式：WAAPI 只动 transform，不会碰 transform-origin。
    node.style.transformOrigin = originCss(layer.origin);
    // 关键：初始样式是「终态」。没有 WAAPI、或动画被丢弃时，元素必须原地可见，
    // 而不是停在 opacity:0 上——那会让整页看起来空白，且没有任何报错。
    node.style.opacity = '1';

    const img = document.createElement('img');
    img.className = 'ppts-layer-image';
    img.src = layer.image;
    img.alt = '';
    img.decoding = 'async';
    node.append(img);
    container.append(node);

    let animation: Animation | null = null;
    const frames = compileKeyframes(layer.clips ?? [], opts, layer.elementId);
    if (canAnimate && frames) {
      animation = node.animate(frames, {
        duration: Math.max(1, opts.pageDurationMs),
        // fill: both —— 时间在关键帧区间之外时保持端点值。
        // 没有它，元素会在自己的 clip 开始前跳回初始样式。
        fill: 'both',
        easing: 'linear' // 采样点之间线性：与 30fps 成片的逐帧定义一致
      });
      animation.pause();
      animation.currentTime = 0;
    }
    out.push({ element: node, animation });
  }
  return out;
}

/** 把一个已编译的层设到页内某个时刻（毫秒）。 */
export function seekLayer(layer: CompiledLayer, localMs: number): void {
  if (!layer.animation) return;
  const duration = Number(layer.animation.effect?.getTiming().duration ?? 0);
  const t = Math.max(0, Math.min(duration > 0 ? duration : 0, localMs));
  layer.animation.currentTime = t;
}

/** 取消并释放。留着 pause 的 animation 会继续占合成层，20 页之后是几百个对象。 */
export function releaseLayers(layers: CompiledLayer[]): void {
  for (const layer of layers) {
    layer.animation?.cancel();
    layer.element.remove();
  }
  layers.length = 0;
}

function pct(value: number, total: number): string {
  if (!(total > 0)) return '0%';
  return `${(value / total) * 100}%`;
}

function clamp01(v: number): number {
  if (!isFinite(v)) return 1;
  return v < 0 ? 0 : v > 1 ? 1 : v;
}

function round(v: number): number {
  return Math.round(v * 1000) / 1000;
}
