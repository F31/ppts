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

import { FOCUS_FADE_MS, evaluateLayer, focusDim, focusMasks, focusOpacity, originCss } from '../../../shared/motion.ts';
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
 * 焦点过渡窗口内的采样步长（毫秒）。
 *
 * 为什么过渡要单独加密采样、而不是沿用「每帧一个点」：整页按帧采样是对的，
 * 因为 MP4 在帧之间没有定义；但过渡曲线（easeOutCubic）在 240ms 里弯得最厉害，
 * 而 WAAPI 在关键帧之间是**线性**插值。只取两个端点时，网页版的淡入是一条直线：
 * 真浏览器实测（`tmp/lowpower-probe.mjs`，焦点 1000ms 开始、240ms 过渡）读到
 * 0.1125 / 0.225 / 0.3375 / 0.45，而同一时刻成片是 0.260 / 0.394 / 0.443 / 0.45
 * ——中点上差 0.169，是满纱（0.45）的三分之一还多。8ms 的弦与曲线的差 < 0.001
 * alpha，一次过渡也就三十来个关键帧。
 */
const FADE_SAMPLE_MS = 8;

/** 把一段过渡窗口的采样时刻追加进 times（含两端）。 */
function pushFadeSamples(times: number[], at: number, fade: number): void {
  if (!isFinite(at) || !isFinite(fade)) return;
  if (fade <= 0) {
    times.push(at); // 硬切：窗口没有宽度，端点就是全部
    return;
  }
  for (let t = at; t < at + fade; t += FADE_SAMPLE_MS) times.push(t);
  times.push(at + fade);
}

/** 去重并夹到 [0,pageMs]，且必须严格递增（WAAPI 遇到重复 offset 会跳一下）。 */
function timelinePoints(marks: number[], pageMs: number): number[] {
  const points: number[] = [];
  for (const t of marks) {
    const clamped = Math.max(0, Math.min(pageMs, t));
    if (points.length === 0 || clamped > points[points.length - 1]) points.push(clamped);
  }
  return points;
}

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
  //
  // 过渡**窗口内部**同样要采样：曲线是 easeOutCubic，而 WAAPI 在关键帧之间做的是
  // 线性插值。只给两个端点等于把曲线换成它的弦——240ms 过渡的中点，成片里「让位」
  // 的那个已经压到 0.61、接手的已经亮到 0.94，网页版两个都还停在 0.775：
  // 看起来像「谁也没被指到」。两端都不报错，并排才看得出。
  //
  // 只给「被焦点指过的元素」插：其余层的 focusDim 恒为 0，插了也只是白涨关键帧。
  const dimmed = elementId ? (opts.focus ?? []).some((f) => f.elementId === elementId) : false;
  if (dimmed) {
    for (const f of opts.focus ?? []) {
      const fade = f.fadeMs ?? FOCUS_FADE_MS;
      pushFadeSamples(times, f.startMs, fade);
      // 放手也是一个窗口，理由同上：漏了就会被线性插值抹成一路慢慢变亮。
      if (f.endMs) pushFadeSamples(times, f.endMs, fade);
    }
  }
  times.push(pageMs);
  const points = timelinePoints(times, pageMs);
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
  // 遮罩在**所有层之后**追加：它压暗的是画面，不是某一层。
  out.push(...buildFocusMasks(container, canvasW, canvasH, opts, canAnimate));
  return out;
}

/**
 * 这一页有没有「矩形焦点」——即需要靠遮罩来指的目标。
 *
 * 单独导出是因为**低电量降级要按它做例外**：低内存设备不建元素层，但焦点遮罩得留下
 * （见 main.ts 的 lowPower 分支）。判据只有一份，两处不能各写一个「rect.w > 0」。
 */
export function hasRectFocus(focus?: FocusSpan[]): boolean {
  return (focus ?? []).some((f) => !!f.rect && f.rect.w > 0 && f.rect.h > 0);
}

/**
 * 这一页需不需要建「图层舞台」。
 *
 * 不只看层数：矩形焦点是独立于图层的增强（柱子没有独立图层），一页完全可能
 * **只有焦点、没有层**。只看 `layers.length` 会让这类页连遮罩都不建——
 * 同一份清单，MP4 在指、网页版全亮，而且两边都没有报错。
 */
export function needsLayerStage(layers: readonly ManifestLayer[], focus?: FocusSpan[]): boolean {
  if (layers.length > 0) return true;
  return hasRectFocus(focus);
}

/**
 * 这一页该建哪些层；返回 null 表示「这一页不用建舞台」。
 *
 * 把「低电量怎么降」收在一个函数里，是为了让它在 CI 里可测：降级判断若散在 main.ts
 * 的两个 if 里，只能靠人读代码相信它，而它要保证的恰恰是「低端机也能看到焦点」——
 * 一件没人会特意去低端机上验的事。
 *
 * 低电量（lowPower）省的是**元素层**：合成层与图片解码是这个形态的主要成本。
 * 焦点遮罩不省——它只是每组一个 div 加一条合成器跑的 opacity，却是「这段在讲哪一根」
 * 的唯一画面提示；整页一起跳过会让低端机永远全亮，既不报错，也与成片不一致。
 */
export function pickStageLayers(
  layers: readonly ManifestLayer[],
  focus: FocusSpan[] | undefined,
  lowPower: boolean
): readonly ManifestLayer[] | null {
  if (!needsLayerStage(layers, focus)) return null;
  if (!lowPower) return layers;
  // 低电量：元素层一个不放。若这页连矩形焦点都没有，舞台上就什么都不剩，不建。
  return hasRectFocus(focus) ? [] : null;
}

/**
 * 一段矩形焦点的遮罩关键帧（纯函数，可在 node 里跑）。
 *
 * 抽出来是因为「网页版在成片的每个帧时刻等于成片」必须能被 CI 核对，而
 * buildFocusMasks 要 DOM 与 WAAPI。这条等价关系此前**只在端点成立**：关键帧
 * 只有 [起点, 起点+fade]，中间靠 WAAPI 线性插值，而成片逐帧求的是 easeOutCubic。
 * 结果是「同一个函数」这句话在过渡区间里不成立，且两端都不报错。
 *
 * 只认这一段自己的 alpha：认领靠的是 `focusMasks` 返回的 `index`，**不是矩形本身**
 * ——两段的矩形原点完全可以相同（同列上下堆叠的两个形状、卡片与压在它上面的标题块），
 * 按 x/y 认领会让两个节点互相认领：孔变成两块矩形的并集（指着标题却把整张卡片
 * 一起留白），孔外白纱叠成 1-(1-0.45)² = 0.6975，整页比设计的更白。元素 id 也不够
 * ——一页里回到同一个元素讲第二次是常见编法，`startMs` 也没有唯一性保证。下标是
 * 这里唯一可靠的归属。
 *
 * 换段那 240ms 里**两个节点同时非零**（旧孔收拢、新孔张开），这是有意的：若旧节点
 * 在新段开口那一刻归零，整页会硬闪一帧全亮。所以这一段自己的关键帧要覆盖下一段的
 * 淡入窗口，而不是在 `next.startMs` 处补一个归零点。
 */
export function focusMaskKeyframes(
  focus: FocusSpan[] | undefined,
  index: number,
  pageMs: number
): Keyframe[] | null {
  const span = focus?.[index];
  const rect = span?.rect;
  if (!focus || !span || !rect || !(rect.w > 0) || !(rect.h > 0) || !(pageMs > 0)) return null;
  const fade = span.fadeMs ?? FOCUS_FADE_MS;
  const marks: number[] = [0];
  pushFadeSamples(marks, span.startMs, fade);
  if (span.endMs) pushFadeSamples(marks, span.endMs, fade);
  // 换段窗口必须整段采样：这一段的孔在下一段淡入的 240ms 里收拢，曲线是
  // easeOutCubic；只补端点又会被 WAAPI 拉成一条弦（正是 5f5f90d 修的那个病）。
  // 也不能在 `next.startMs` 补归零点——那一刻它仍是满的，归零会让整页闪一帧全亮。
  const next = focus[index + 1];
  if (next) pushFadeSamples(marks, next.startMs, next.fadeMs ?? FOCUS_FADE_MS);
  marks.push(pageMs);
  const frames: Keyframe[] = timelinePoints(marks, pageMs).map((t) => {
    // 同一时刻可能有两块（换段窗口）：只取自己那一段，别的由它自己的节点画。
    const mine = focusMasks(focus, t).find((m) => m.index === index);
    return { offset: t / pageMs, opacity: String(round(mine ? mine.alpha : 0)) };
  });
  return frames.length >= 2 ? frames : null;
}

/**
 * 矩形焦点遮罩：每个带 `rect` 的焦点一个节点（整页白纱、rect 处留孔）。
 *
 * 为什么每段一个节点、而不是一个节点换位置：拆成多个节点后每段的关键帧只有
 * 「淡入—保持—收拢」，不需要在切换点搬动矩形，也就不会在换柱时出现「白纱从这根
 * 滑到那根」的中间态。换段那 240ms 里两个节点同时非零（旧的收、新的开），
 * 合成压暗量始终在 0.45 附近——见 shared/motion.ts 的 focusMasks。
 *
 * alpha 由 `focusMasks` 逐采样点求出——和成片渲染器**同一个函数**。若这里另写一套
 * 时间逻辑，网页版和 MP4 迟早会在「什么时候压下去」上分叉，而这种分叉没有报错。
 */
function buildFocusMasks(
  container: HTMLElement,
  canvasW: number,
  canvasH: number,
  opts: CompileOptions,
  canAnimate: boolean
): CompiledLayer[] {
  const out: CompiledLayer[] = [];
  const pageMs = opts.pageDurationMs > 0 ? opts.pageDurationMs : 0;
  const focus = opts.focus ?? [];
  // 用下标遍历：每一段的归属就是它在数组里的位置（见 focusMaskKeyframes），
  // 遮罩节点的顺序与 focus 一一对应，认领不需要再看矩形。
  for (let i = 0; i < focus.length; i++) {
    const rect = focus[i].rect;
    if (!rect || !(rect.w > 0) || !(rect.h > 0)) continue;
    const node = document.createElement('div');
    node.className = 'ppts-focus-mask';
    // `position:absolute` 不能省：left/top 只对定位元素生效。`.ppts-layer` 的那条
    // 规则来自样式表，而遮罩是新加的类、样式表里没有它——漏掉这一句时 left/top
    // 被静默忽略，div 退化成一个 0 尺寸的盒子，box-shadow 从左上角铺满全页：
    // 结果是整页均匀压暗、孔根本不存在。这一条是浏览器截图抓出来的，静态检查全绿。
    node.style.position = 'absolute';
    node.style.pointerEvents = 'none';
    node.style.left = pct(rect.x, canvasW);
    node.style.top = pct(rect.y, canvasH);
    node.style.width = pct(rect.w, canvasW);
    node.style.height = pct(rect.h, canvasH);
    // 一个透明的 div + 无穷大的阴影 = 中间留孔的白纱。孔边用 blur 羽化：
    // 硬边会在网格线上切出一亮一淡的接缝。
    node.style.boxShadow = '0 0 0 9999px #fff';
    node.style.filter = 'blur(8px)';
    // 初始「不压暗」是终态：没有 WAAPI 时只是少一层遮罩，不会把整页盖白。
    node.style.opacity = '0';
    container.append(node);

    let animation: Animation | null = null;
    if (canAnimate && pageMs > 0) {
      const frames = focusMaskKeyframes(focus, i, pageMs);
      if (frames) {
        animation = node.animate(frames, {
          duration: Math.max(1, pageMs),
          fill: 'both',
          easing: 'linear'
        });
        animation.pause();
        animation.currentTime = 0;
      }
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
