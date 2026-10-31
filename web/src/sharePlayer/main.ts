// 分享成品播放器（M6-1）。
//
// 设计约束（见 openspec/changes/html-share-bundle-static/design.md）：
//   - 编译为 IIFE 单文件 app.js + app.css，用经典 <script>/<link> 引入：file:// 下
//     fetch 与 <script type="module"> 会被 CORS 拦住，"双击就能播"只能走这条路。
//   - 时间数学单源：直接复用 ../playerClock 的 slideAt/subtitleAt/spokenCharAt/splitLines/progress，
//     不在这里重新实现一套（否则 HTML 与站内播放器/MP4 烧录字幕会各自漂移）。
//   - 权威时钟是 <audio>.currentTime；rAF 只负责把时钟读出来画到 DOM，绝不自己累加时间。
import { progress, slideAt, spokenCharAt, splitLines, subtitleAt, usecToClock } from '../playerClock';
import type { SlideCue, SubtitleCue, Timeline } from '../types';
import { easeOutCubic } from '../../../shared/motion.ts';
import { shotAt, cameraTransform } from '../../../shared/shots.ts';
import type { FocusSpan, Shot } from '../../../shared/types';
import { buildLayerNodes, releaseLayers, seekLayer } from './animation';
import type { CompiledLayer, ManifestLayer } from './animation';
import './player.css';

/** 播放器认识的清单版本。读到更高版本必须拒绝——猜着播比不播更糟。 */
const MAX_SCHEMA_VERSION = 2;

type ShareSlide = SlideCue & {
  image: string;
  /** image 是否为挖空底板（元素位置已被填平，由 layers 盖回去）。 */
  plate?: boolean;
  transition?: { type: string; durationMs?: number };
  layers?: ManifestLayer[];
  /** 页内镜头切片。缺省表示整页一张图（旧包与不切镜的页都是这种）。 */
  shots?: Shot[];
  /** 页级焦点序列：此刻讲的是谁（与 MP4 同一份求值）。 */
  focus?: FocusSpan[];
};

type ShareAudio = {
  segmentId: string;
  src: string;
  startUs: number;
  endUs: number;
};

type ShareManifest = {
  schemaVersion: number;
  title?: string;
  video: { width: number; height: number; fps: number };
  slides: ShareSlide[];
  audio: ShareAudio[];
  // audioMix 为按时间轴落位的整段音轨（含段间静音），播放器优先使用它：
  // 单一 currentTime 直接等于全局时间，seek 与字幕对齐都不需要额外换算。
  audioMix?: string;
  subtitles: { src: string; inline: string };
  artifact: { durationUs: number; snapshotHash?: string };
};

declare global {
  interface Window {
    __PPT_MANIFEST__?: ShareManifest;
    __PPT_SUBTITLES__?: SubtitleCue[];
    /** 精确定位（微秒）与精确读数。仅供截图探针，见赋值处的说明。 */
    __PPT_SEEK_US__?: (us: number) => void;
    __PPT_POSITION_US__?: () => number;
  }
}

const PLAY_GLYPH = '\u25B6';
const PAUSE_GLYPH = '\u275A\u275A';
const REPLAY_GLYPH = '\u21BB';

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className?: string,
  text?: string
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function mount(): void {
  const manifest = window.__PPT_MANIFEST__;
  const host = document.getElementById('ppts-share-root');
  if (!host || !manifest || !Array.isArray(manifest.slides) || manifest.slides.length === 0) {
    return;
  }

  // 清单版本高于本播放器认识的版本时必须明确拒绝：v2 的 image 语义是「挖空底板」，
  // 按 v1 的逻辑播会露出一块纯色疤。宁可说"需要更新的播放器"，也不要静默播错。
  if (typeof manifest.schemaVersion === 'number' && manifest.schemaVersion > MAX_SCHEMA_VERSION) {
    host.textContent = '该成品包由更新版本的播放器生成，请重新导出或更新播放器后再打开。';
    return;
  }

  const subtitles = Array.isArray(window.__PPT_SUBTITLES__) ? window.__PPT_SUBTITLES__ : [];
  const durationUs = Math.max(0, manifest.artifact.durationUs);
  const timeline: Timeline = {
    schemaVersion: '1.0',
    durationUs,
    slides: manifest.slides,
    subtitles
  };
  const title = manifest.title || document.title || '讲解视频';

  document.documentElement.classList.add('ppts-share-page');
  document.body.classList.add('ppts-share-page');

  // ---- DOM 骨架（不使用 innerHTML，PPT 文本一律走 textContent 转义）----
  const root = el('div', 'ppts-share');
  const stage = el('div', 'ppts-stage');
  // 舞台按成片宽高比铺，层的百分比定位才是准确的（写死 16/9 会让非 16:9 的页错位）。
  stage.style.aspectRatio = `${manifest.video.width} / ${manifest.video.height}`;
  // 两个面板：当前页与「上一页的末帧」。转场时两者叠加，与渲染器的 PageEntry
  // 同结构（前一页取末帧 + 当前页交叉），否则页首那一帧两端必然对不上。
  //
  // 每个面板里是**两张**叠放的图：`img` 是底层（当前镜头），`imgTop` 只在页内
  // 换镜的叠化窗口里出现（后一镜）。层在两张图之上，换镜时不重建——层的 clips
  // 是页内相对毫秒，重建会让已经入场的元素重新入场一次（design §4）。
  const makePanel = (): {
    panel: HTMLElement;
    img: HTMLImageElement;
    imgTop: HTMLImageElement;
    layers: HTMLElement;
  } => {
    const panel = el('div', 'ppts-panel');
    const img = el('img', 'ppts-slide');
    img.alt = title;
    img.decoding = 'async';
    const imgTop = el('img', 'ppts-slide');
    // 叠化时它盖在 img 上；不需要时 hidden。CSS 里有 `.ppts-slide[hidden]` 兜底，
    // 否则作者样式的 display:block 会盖掉 UA 的 [hidden]——图会一直显示。
    imgTop.alt = '';
    imgTop.decoding = 'async';
    imgTop.hidden = true;
    const layers = el('div', 'ppts-layers');
    panel.append(img, imgTop, layers);
    return { panel, img, imgTop, layers };
  };
  const prev = makePanel();
  const cur = makePanel();
  const layersBox = cur.layers;
  const image = cur.img;
  // 首屏先放第一张图，避免等第一次 render 才出画面（rAF 之前是空白）。
  // dataset.shot 一并写上：syncShot 靠它去重，漏写会让首帧白解码一次。
  image.dataset.shot = manifest.slides[0].image;
  image.src = manifest.slides[0].image;
  prev.panel.hidden = true;

  const cover = el('button', 'ppts-cover');
  cover.type = 'button';
  cover.setAttribute('aria-label', `播放：${title}`);
  const coverGlyph = el('span', 'ppts-cover-glyph', PLAY_GLYPH);
  coverGlyph.setAttribute('aria-hidden', 'true');
  const coverLabel = el('span', 'ppts-cover-label', '点击播放');
  cover.append(coverGlyph, coverLabel);

  const subtitleBox = el('div', 'ppts-subtitle');
  subtitleBox.setAttribute('aria-live', 'off');
  subtitleBox.hidden = true;
  const spoken = el('span', 'ppts-spoken');
  const unspoken = el('span', 'ppts-unspoken');
  subtitleBox.append(spoken, unspoken);

  stage.append(prev.panel, cur.panel, cover, subtitleBox);

  const controls = el('div', 'ppts-controls');
  const prevBtn = el('button', 'ppts-btn', '\u2039');
  prevBtn.type = 'button';
  prevBtn.setAttribute('aria-label', '上一页');
  const playBtn = el('button', 'ppts-btn', PLAY_GLYPH);
  playBtn.type = 'button';
  playBtn.setAttribute('aria-label', '播放');
  const nextBtn = el('button', 'ppts-btn', '\u203A');
  nextBtn.type = 'button';
  nextBtn.setAttribute('aria-label', '下一页');
  const timeLabel = el('span', 'ppts-time', `0:00.0 / ${usecToClock(durationUs)}`);
  const seekBar = el('input', 'ppts-seek');
  seekBar.type = 'range';
  seekBar.min = '0';
  seekBar.max = '1000';
  seekBar.value = '0';
  seekBar.step = '1';
  seekBar.setAttribute('aria-label', '播放进度');
  const pageLabel = el('span', 'ppts-page', `1 / ${manifest.slides.length}`);
  const ccBtn = el('button', 'ppts-btn', '字幕');
  ccBtn.type = 'button';
  ccBtn.setAttribute('aria-label', '字幕开关');
  ccBtn.setAttribute('aria-pressed', 'true');
  controls.append(prevBtn, playBtn, nextBtn, timeLabel, seekBar, pageLabel, ccBtn);

  root.append(stage, controls);
  host.replaceChildren(root);

  // ---- 音频与时钟 ----
  const audio = document.createElement('audio');
  audio.preload = 'auto';
  const hasAudio = Boolean(manifest.audioMix) || manifest.audio.length > 0;
  const mixMode = Boolean(manifest.audioMix);
  let segIndex = -1;
  let playing = false;
  let raf = 0;
  let ended = false;
  // 无音频时的墙钟基准（降级路径：仍能看到翻页与字幕轮换）。
  let wallBaseUs = 0;
  let wallStartedAt = 0;
  let wallRunning = false;

  const segmentAt = (us: number): number => {
    let found = -1;
    for (let i = 0; i < manifest.audio.length; i++) {
      if (manifest.audio[i].startUs <= us) found = i;
      else break;
    }
    return found < 0 ? 0 : found;
  };

  const loadSegment = (index: number): void => {
    const seg = manifest.audio[index];
    if (!seg) return;
    segIndex = index;
    audio.src = seg.src;
  };

  // 音频源必须在任何 play() 之前就位：mix 模式下是一个整段音轨，
  // 否则 <audio> 没有 src，play() 必然 reject，页面永远停在封面上。
  if (hasAudio) {
    if (mixMode) {
      audio.src = manifest.audioMix as string;
    } else if (manifest.audio.length > 0) {
      loadSegment(0);
    }
  }
  // 元数据未就绪时设置的 currentTime 会被丢弃，记下来在 loadedmetadata 后补一次。
  let pendingSeekUs = -1;

  const wallPosition = (): number => {
    if (!wallRunning) return wallBaseUs;
    return clamp(wallBaseUs + (performance.now() - wallStartedAt) * 1000, 0, durationUs);
  };

  const positionUs = (): number => {
    if (!hasAudio) return wallPosition();
    const current = Math.max(0, audio.currentTime * 1e6);
    if (mixMode) return clamp(current, 0, durationUs || current);
    const seg = manifest.audio[segIndex];
    if (!seg) return 0;
    return clamp(seg.startUs + current, seg.startUs, seg.endUs);
  };

  // ---- 动画层 ----
  const videoW = manifest.video.width > 0 ? manifest.video.width : 1920;
  const videoH = manifest.video.height > 0 ? manifest.video.height : 1080;
  // 降级只去掉位移与缩放，保留 opacity，且**时长不变**——缩短时长会让动画提前结束，
  // 与音频、字幕错位，那比"少动一点"严重得多。
  const reduceMotion =
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  // 低内存设备不建动画层：合成层与图片解码是这个形态的主要成本，而它换来的只是
  // "动效更好看"。navigator.deviceMemory 只有 Chromium 系提供（0.25/0.5/1/2/4/8 档），
  // 拿不到就不降级——不能因为"检测不到"就替所有 Safari/Firefox 用户选最保守的形态。
  // 注意与 reduceMotion 的差别：后者保留 opacity 动画，这里连 opacity 都不做。
  const deviceMemory = (navigator as Navigator & { deviceMemory?: number }).deviceMemory;
  const lowPower = typeof deviceMemory === 'number' && deviceMemory < 4;
  let activeLayers: CompiledLayer[] = [];
  let activeSlideStartUs = 0;
  // 转场中的「前一页末帧」。MP4 的 PageEntry 不是只换图，而是把前一页的最后一帧
  // 垫在当前页下面做交叉；DOM 若只换图，页首那一帧必然对不上（实测 fade 页首
  // SSIM 0.85–0.91，其余时点 ≥0.9936）。
  let prevLayers: CompiledLayer[] = [];
  let transitionKind = 'none';
  let transitionMs = 0;

  // --ppts-k = 1 画布像素等于多少实际像素。层的位移在清单里是画布像素，
  // 舞台按比例缩放，所以关键帧里的 translate 要乘这个系数（见 animation.ts）。
  const syncStageScale = (): void => {
    const width = stage.clientWidth;
    if (width > 0 && videoW > 0) stage.style.setProperty('--ppts-k', String(width / videoW));
  };
  syncStageScale();
  // 窗口尺寸变了要重算 --ppts-k，否则层位移会按旧比例缩放（画面"错位一点点"，
  // 而这种错位在小窗里几乎看不出来）。
  window.addEventListener('resize', syncStageScale);

  const rebuildLayers = (slide: ShareSlide): void => {
    // 切页必须 cancel：留着 pause 的 animation 会继续占合成层，20 页之后是几百个对象，
    // 而且旧层的图片还挂在 DOM 上会盖住新页。
    releaseLayers(activeLayers);
    activeSlideStartUs = slide.startUs;
    const layers = slide.layers ?? [];
    if (layers.length === 0 || lowPower) return;
    activeLayers = buildLayerNodes(layersBox, layers, videoW, videoH, {
      fps: manifest.video.fps > 0 ? manifest.video.fps : 30,
      pageDurationMs: (slide.endUs - slide.startUs) / 1000,
      reduceMotion,
      focus: slide.focus
    });
  };

  const syncLayers = (us: number): void => {
    if (activeLayers.length === 0) return;
    const localMs = (us - activeSlideStartUs) / 1000;
    for (const layer of activeLayers) seekLayer(layer, localMs);
  };

  // ---- 页内镜头（换底图）----
  //
  // 与切页是两件不同的事：切页换面板、重建层；换镜只换图，层继续跑。
  // 这里不碰任何 WAAPI，所以「无 WAAPI / reduced-motion / 低电量」三种降级下
  // 镜头照样切——降级的是动画，不是画面（tasks 2.4）。
  //
  // 两张图各自记住自己当前的 src（dataset.shot）。逐帧比对再赋值，而不是
  // 每帧无条件写 src：重复赋值同一个 URL 会让浏览器重新解码，30fps 下就是
  // 满屏的掉帧——而掉帧看起来像"动画卡了"，不像"代码写错了"。
  let shotOnStage = false;
  let preloaded: HTMLImageElement[] = [];

  const setShotSrc = (img: HTMLImageElement, path: string): void => {
    if (img.dataset.shot === path) return;
    img.dataset.shot = path;
    img.src = path;
  };

  const showTop = (path: string, opacity: number): void => {
    setShotSrc(cur.imgTop, path);
    cur.imgTop.hidden = false;
    cur.imgTop.style.opacity = String(opacity);
  };

  const hideTop = (): void => {
    if (cur.imgTop.hidden && !cur.imgTop.getAttribute('src')) return;
    cur.imgTop.hidden = true;
    cur.imgTop.style.opacity = '0';
    // 连 src 一起清掉：留着的话它会一直出现在「当前画面上有哪些图」的读数里
    // （截图探针读到两张，判据就分不清哪张才是画出来的那张）。清掉是安全的——
    // 重新显示总是从叠化的 opacity 0 开始，解码那几毫秒本就看不见。
    cur.imgTop.removeAttribute('src');
    delete cur.imgTop.dataset.shot;
  };

  /**
   * 把「这一页此刻该显示哪张图」落到 DOM。`localMs` 是页内相对毫秒。
   *
   * 没有镜头的页走第一条分支：底图仍是页级 `image`，叠化层清空，层的可见性复原。
   * 画面与引入镜头之前一致——DOM 上多了一个空的 `<img>`（每个面板一个，无 src、
   * `hidden`），但它不参与绘制。
   */
  /**
   * 把相机变换应用到一张镜头图上：在镜内相对时间插值出视口中心与缩放，写 `transform`。
   * 舞台已 overflow:hidden，scale≥1 时图始终盖住舞台、不会露白。无相机时清掉 transform
   * （否则上一镜的缩放会残留在这一镜上）。
   */
  const applyCamera = (img: HTMLImageElement, shot: Shot, localMs: number): void => {
    const cam = shot.camera;
    if (!cam || cam.length === 0) {
      img.style.transform = '';
      return;
    }
    const { x, y, scale } = cameraTransform(cam, localMs - shot.startMs);
    const tx = (0.5 - x) * videoW * scale;
    const ty = (0.5 - y) * videoH * scale;
    img.style.transform =
      `translate(calc(${tx}px * var(--ppts-k, 1)), calc(${ty}px * var(--ppts-k, 1))) scale(${scale})`;
  };

  const syncShot = (slide: ShareSlide, localMs: number): void => {
    const shots = slide.shots ?? [];
    const has = shots.length > 0;
    if (has !== shotOnStage) {
      shotOnStage = has;
      // 有镜头的页里 image 必须铺满舞台（design §4）：特写图的宽高比通常与原页
      // 不同，contain 会留黑边，「特写」就成了「中间一小块」。
      for (const node of [cur.img, cur.imgTop]) node.classList.toggle('ppts-shot', has);
    }

    const frame = shotAt(shots, localMs);
    if (!frame) {
      setShotSrc(cur.img, slide.image);
      cur.img.style.transform = '';
      hideTop();
      layersBox.style.visibility = '';
      return;
    }

    const fading = frame.from !== undefined && frame.progress < 1;
    if (fading && frame.from) {
      // 叠化：底层留旧镜，顶层是新镜并按进度淡入。进度走到 1 时两图相同，
      // 所以此刻换底层不会有可看见的跳变。
      setShotSrc(cur.img, frame.from.image);
      showTop(frame.shot.image, frame.progress);
    } else {
      setShotSrc(cur.img, frame.shot.image);
      hideTop();
    }

    // 相机运动：镜内相对时间插值，写到底层与（叠化中的）顶层。无相机时 applyCamera 清 transform。
    applyCamera(cur.img, frame.from ?? frame.shot, localMs);
    if (fading && frame.from) applyCamera(cur.imgTop, frame.shot, localMs);

    // Crop 非空的镜头里层 MUST NOT 显示：层的 box 是整页坐标，特写把局部放大后
    // 同一套坐标必然错位，而错位不会报错，只会看起来「元素飘了」（design §4）。
    // 用 visibility 而不是 display：层还在跑自己的动画，切回全图时不该从零重建。
    layersBox.style.visibility = frame.shot.crop ? 'hidden' : '';
  };

  /** 切页时预取这一页的镜头图。不预取的话，叠化的第一帧拍到的是还没解码的空白。 */
  const preloadShots = (slide: ShareSlide): void => {
    preloaded = [];
    for (const shot of slide.shots ?? []) {
      if (!shot.image) continue;
      const img = new Image();
      img.src = shot.image;
      // 持有引用：丢掉的对象可能在请求完成前被回收，缓存里就什么也没有。
      preloaded.push(img);
    }
  };

  /** 一页「停在最后一帧」时显示的是哪张图——切页转场要拿它垫底。 */
  const lastShotImage = (slide: ShareSlide): string => {
    const shots = slide.shots ?? [];
    return shots.length > 0 ? shots[shots.length - 1].image : slide.image;
  };

  const lastShotIsCrop = (slide: ShareSlide): boolean => {
    const shots = slide.shots ?? [];
    return shots.length > 0 && Boolean(shots[shots.length - 1].crop);
  };

  const clearPrev = (): void => {
    releaseLayers(prevLayers);
    prev.panel.hidden = true;
    prev.panel.style.opacity = '1';
    prev.panel.style.transform = 'none';
    prev.img.style.transform = '';
    prev.layers.style.visibility = '';
    transitionKind = 'none';
    transitionMs = 0;
  };

  /** 切页时把「前一页的末帧」摆好；没有转场就保持隐藏，不白建图层。 */
  const prepareTransition = (before: ShareSlide | undefined, slide: ShareSlide): void => {
    clearPrev();
    const kind = slide.transition?.type ?? 'none';
    const ms = slide.transition?.durationMs ?? 0;
    if (!before || kind === 'none' || ms <= 0) return;
    // 前一页的「末帧」是它最后一个镜头的图，不是页级 image：切到特写的页，
    // 页尾停在特写上，拿整页图去垫底，交叉的第一帧就与成片差一整张画面。
    setShotSrc(prev.img, lastShotImage(before));
    prev.img.classList.toggle('ppts-shot', (before.shots?.length ?? 0) > 0);
    // 末镜若带相机，转场垫底也按镜末帧的相机取景（否则交叉第一帧未对齐）。
    const lastShot = before.shots?.[before.shots.length - 1];
    if (lastShot) applyCamera(prev.img, lastShot, lastShot.durationMs);
    prev.panel.hidden = false;
    cur.panel.style.opacity = '1';
    cur.panel.style.transform = 'none';
    transitionKind = kind;
    transitionMs = ms;
    const layers = before.layers ?? [];
    // 前一页停在特写镜头上时它的层本来就不显示，交叉时也不能冒出来
    // （否则页首 360ms 会看到整页的层叠在放大后的局部图上）。
    prev.layers.style.visibility = lastShotIsCrop(before) ? 'hidden' : '';
    // 前一页的层同样受低内存约束；否则降级只降了当前页、交叉时又冒出来一层。
    if (layers.length === 0 || lowPower) return;
    const pageMs = (before.endUs - before.startUs) / 1000;
    prevLayers = buildLayerNodes(prev.layers, layers, videoW, videoH, {
      fps: manifest.video.fps > 0 ? manifest.video.fps : 30,
      pageDurationMs: pageMs,
      reduceMotion,
      focus: before.focus
    });
    // 前一页取的是它自己的末帧（与 MP4 的 PageView tMs=durationMs 一致）：
    // 若该页有出场动画，末帧才是正确画面，不能拿底板顶替。
    for (const layer of prevLayers) seekLayer(layer, pageMs);
  };

  /**
   * 按转场进度摆两个面板。三种类型与 renderer/src/Timeline.tsx 逐条对齐：
   * fade 双向透明度、slide 只推入、zoom 淡入 + 轻微放大，未知类型按 fade。
   */
  const applyTransition = (slide: ShareSlide, us: number): void => {
    if (transitionMs <= 0) return;
    const tMs = (us - slide.startUs) / 1000;
    if (tMs >= transitionMs) {
      clearPrev();
      cur.panel.style.opacity = '1';
      cur.panel.style.transform = 'none';
      return;
    }
    const p = easeOutCubic(clamp(tMs / transitionMs, 0, 1));
    switch (transitionKind) {
      case 'slide':
        // 位移量是画布像素，舞台缩放过，同样要乘 --ppts-k。
        cur.panel.style.transform = `translateX(calc(${Math.round((1 - p) * videoW)}px * var(--ppts-k, 1)))`;
        break;
      case 'zoom':
        cur.panel.style.opacity = String(p);
        cur.panel.style.transform = `scale(${1 + 0.06 * (1 - p)})`;
        break;
      default:
        cur.panel.style.opacity = String(p);
        prev.panel.style.opacity = String(1 - p);
        break;
    }
  };

  // ---- 渲染 ----
  let lastSlideID = '';
  let lastCueIndex = -2;
  let lastLineIndex = -2;
  let lastSpoken = -1;
  let lastFill = -1;
  let subtitlesVisible = true;

  const renderSubtitle = (us: number): void => {
    if (!subtitlesVisible) return;
    const cue = subtitleAt(timeline, us);
    if (!cue) {
      subtitleBox.hidden = true;
      lastCueIndex = -1;
      return;
    }
    const cueIndex = subtitles.indexOf(cue);
    const at = spokenCharAt(cue, us);
    const lines = splitLines(cue);
    let lineIndex = lines.findIndex((line) => at >= line.start && at < line.end);
    if (lineIndex < 0) lineIndex = at >= cue.text.length ? lines.length - 1 : 0;
    if (lineIndex < 0) lineIndex = 0;
    if (cueIndex === lastCueIndex && lineIndex === lastLineIndex && at === lastSpoken) {
      subtitleBox.hidden = false;
      return;
    }
    const line = lines[lineIndex] ?? { start: 0, end: 0 };
    const spokenInLine = clamp(at, line.start, line.end) - line.start;
    spoken.textContent = cue.text.slice(line.start, line.start + Math.max(0, spokenInLine));
    unspoken.textContent = cue.text.slice(line.start + Math.max(0, spokenInLine), line.end);
    subtitleBox.hidden = false;
    lastCueIndex = cueIndex;
    lastLineIndex = lineIndex;
    lastSpoken = at;
  };

  const render = (us: number): void => {
    // timeline.slides 就是 manifest.slides（带 image），slideAt 的返回类型只描述时间字段。
    const slide = slideAt(timeline, us) as ShareSlide;
    if (slide.slideId !== lastSlideID) {
      const index = manifest.slides.findIndex((item) => item.slideId === slide.slideId);
      image.alt = title;
      lastSlideID = slide.slideId;
      preloadShots(slide);
      rebuildLayers(slide);
      // 前一页取 index-1 而不是"上一次渲染的页"：拖动进度条跳页时，上一帧
      // 停在哪与时间轴上的前驱无关，按前驱算才对得上成片。
      prepareTransition(index > 0 ? manifest.slides[index - 1] : undefined, slide);
      pageLabel.textContent = `${index + 1} / ${manifest.slides.length}`;
      prevBtn.disabled = index <= 0;
      nextBtn.disabled = index < 0 || index >= manifest.slides.length - 1;
    }
    // 镜头每帧求值（内部按 src 去重）：页内换镜不经过上面那条分支，
    // 只在切页时换图的话，「一页多镜」就只是一层没人看得见的数据。
    // 除以 1000 不能省：us 与 slide.startUs 都是微秒，而镜头的 startMs 是毫秒。
    // 漏除的后果不是报错，而是每一帧都超出末镜——画面永远停在最后一镜。
    syncShot(slide, (us - slide.startUs) / 1000);
    applyTransition(slide, us);
    const ratio = progress(us, durationUs);
    const fill = Math.round(ratio * 1000);
    if (fill !== lastFill) {
      seekBar.value = String(fill);
      seekBar.style.setProperty('--ppts-fill', `${ratio * 100}%`);
      timeLabel.textContent = `${usecToClock(us)} / ${usecToClock(durationUs)}`;
      lastFill = fill;
    }
    syncLayers(us);
    renderSubtitle(us);
  };

  // ---- 播放控制 ----
  const stopLoop = (): void => {
    if (raf) cancelAnimationFrame(raf);
    raf = 0;
  };

  const tick = (): void => {
    if (!playing) return;
    const us = positionUs();
    render(us);
    if (us >= durationUs && durationUs > 0) {
      finish();
      return;
    }
    raf = requestAnimationFrame(tick);
  };

  const startLoop = (): void => {
    stopLoop();
    if (playing) raf = requestAnimationFrame(tick);
  };

  const showCover = (label: string): void => {
    cover.hidden = false;
    coverGlyph.textContent = ended ? REPLAY_GLYPH : PLAY_GLYPH;
    coverLabel.textContent = label;
  };

  const hideCover = (): void => {
    cover.hidden = true;
  };

  const startWall = (fromUs: number): void => {
    wallBaseUs = fromUs;
    wallStartedAt = performance.now();
    wallRunning = true;
  };

  const finish = (): void => {
    playing = false;
    ended = true;
    stopLoop();
    if (hasAudio) audio.pause();
    wallRunning = false;
    playBtn.textContent = REPLAY_GLYPH;
    playBtn.setAttribute('aria-label', '重播');
    render(durationUs);
    showCover('已播完，点击重播');
  };

  const play = (): void => {
    if (ended) {
      ended = false;
      seek(0);
    }
    playing = true;
    hideCover();
    playBtn.textContent = PAUSE_GLYPH;
    playBtn.setAttribute('aria-label', '暂停');
    if (hasAudio) {
      if (!mixMode && segIndex < 0) loadSegment(segmentAt(positionUs()));
      const result = audio.play();
      if (result && typeof result.catch === 'function') {
        // 自动播放被拦：给出封面手势，绝不静默失败。
        result.catch(() => {
          playing = false;
          stopLoop();
          playBtn.textContent = PLAY_GLYPH;
          playBtn.setAttribute('aria-label', '播放');
          showCover('点击播放');
        });
      }
    } else {
      startWall(positionUs());
    }
    startLoop();
  };

  const pause = (): void => {
    playing = false;
    stopLoop();
    if (hasAudio) audio.pause();
    wallBaseUs = wallPosition();
    wallRunning = false;
    playBtn.textContent = PLAY_GLYPH;
    playBtn.setAttribute('aria-label', '播放');
  };

  const seek = (us: number): void => {
    const target = clamp(Math.round(us), 0, durationUs || 0);
    if (hasAudio) {
      if (mixMode) {
        try {
          if (audio.readyState < 1) pendingSeekUs = target;
          else audio.currentTime = target / 1e6;
        } catch {
          pendingSeekUs = target;
        }
      } else {
        const index = segmentAt(target);
        if (index !== segIndex) loadSegment(index);
        const seg = manifest.audio[index];
        if (seg) {
          try {
            if (audio.readyState < 1) pendingSeekUs = Math.max(0, target - seg.startUs);
            else audio.currentTime = Math.max(0, target - seg.startUs) / 1e6;
          } catch {
            pendingSeekUs = Math.max(0, target - seg.startUs);
          }
        }
      }
    } else {
      wallBaseUs = target;
      wallStartedAt = performance.now();
    }
    ended = false;
    render(target);
  };

  // 截图探针用的精确定位入口。滑杆是 0–1000 的千分比：40 分钟的作业上一步
  // 就是 2.4 秒，而门禁要取的「切点前后一帧」只有 33ms——靠滑杆根本定位不到，
  // 而定位不到时截图仍然"成功"，只是取错了时刻（错得看不出来）。
  // 只暴露定位与读数两个函数，不改变任何播放行为：没有它们，探针退回用滑杆。
  window.__PPT_SEEK_US__ = seek;
  window.__PPT_POSITION_US__ = positionUs;

  const stepSlide = (delta: number): void => {
    const current = slideAt(timeline, positionUs());
    const index = manifest.slides.findIndex((slide) => slide.slideId === current.slideId);
    const next = manifest.slides[index + delta];
    if (!next) return;
    seek(next.startUs);
  };

  playBtn.addEventListener('click', () => {
    if (playing) pause();
    else play();
  });
  cover.addEventListener('click', play);
  prevBtn.addEventListener('click', () => stepSlide(-1));
  nextBtn.addEventListener('click', () => stepSlide(1));
  seekBar.addEventListener('input', () => {
    const ratio = Number(seekBar.value) / 1000;
    seek(ratio * durationUs);
  });
  ccBtn.addEventListener('click', () => {
    subtitlesVisible = !subtitlesVisible;
    ccBtn.setAttribute('aria-pressed', subtitlesVisible ? 'true' : 'false');
    if (!subtitlesVisible) {
      subtitleBox.hidden = true;
      lastCueIndex = -2;
    } else {
      render(positionUs());
    }
  });

  document.addEventListener('keydown', (event) => {
    if (event.defaultPrevented) return;
    const target = event.target as HTMLElement | null;
    if (target && /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName)) return;
    if (event.code === 'Space') {
      event.preventDefault();
      if (playing) pause();
      else play();
    } else if (event.code === 'ArrowLeft') {
      event.preventDefault();
      stepSlide(-1);
    } else if (event.code === 'ArrowRight') {
      event.preventDefault();
      stepSlide(1);
    }
  });

  audio.addEventListener('ended', () => {
    if (!hasAudio) return;
    if (mixMode) {
      finish();
      return;
    }
    // 无混音轨时的降级路径：逐段续播（段间静音不插入，时间轴总长会略短）。
    const next = segIndex + 1;
    if (next < manifest.audio.length) {
      loadSegment(next);
      void audio.play().catch(() => undefined);
    } else {
      finish();
    }
  });
  audio.addEventListener('loadedmetadata', () => {
    if (pendingSeekUs >= 0) {
      try {
        audio.currentTime = pendingSeekUs / 1e6;
      } catch {
        /* 仍未就绪：保留 pending，等下一次 loadedmetadata（切段时会重新触发） */
      }
      pendingSeekUs = -1;
    }
    if (!playing) render(positionUs());
  });

  render(0);
  prevBtn.disabled = true;
  nextBtn.disabled = manifest.slides.length <= 1;

  // 首屏尝试自动播放：浏览器允许就直接播，被拦截则停在封面等一次点击。
  // 注意这里不能复用 play()——play() 会再调一次 audio.play()，与本次尝试互相抢占。
  if (hasAudio) {
    const autoplay = audio.play();
    if (autoplay && typeof autoplay.then === 'function') {
      autoplay.then(
        () => {
          playing = true;
          hideCover();
          playBtn.textContent = PAUSE_GLYPH;
          playBtn.setAttribute('aria-label', '暂停');
          startLoop();
        },
        () => showCover('点击播放')
      );
    } else {
      showCover('点击播放');
    }
  } else {
    showCover('点击播放');
  }
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', mount, { once: true });
} else {
  mount();
}
