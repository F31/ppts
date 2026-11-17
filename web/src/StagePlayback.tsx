import { useEffect, useRef, useState } from 'react';
import { cameraTransform, shotAt } from '../../shared/shots.ts';
import type { Shot } from '../../shared/types.ts';
import type { AnimationPreviewScene } from './api/animation';
import { useI18n } from './i18n';
import {
  applyLookCrop,
  buildLayerNodes,
  needsLayerStage,
  releaseLayers,
  seekLayer,
  type CompiledLayer,
  type ManifestLayer
} from './sharePlayer/animation';

function formatClock(ms: number): string {
  const clamped = Math.max(0, ms);
  const total = Math.floor(clamped / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  const tenth = Math.floor((clamped % 1000) / 100);
  return `${minutes}:${String(seconds).padStart(2, '0')}.${tenth}`;
}

/**
 * 在编辑页中间主舞台播放一页动效。
 *
 * 求值、镜头和遮罩都复用分享播放器的那一份：另写一套插值，预览和成片就会在
 * 叠加 clip 或焦点淡入上分叉，而且两边都不报错。不走低电量降级——编辑者点预览
 * 就是要看元素层。这里代表导出结果，不跟 prefers-reduced-motion 去掉位移和缩放；
 * 分享页播放器仍按系统设置降级。
 */
export function StagePlayback({ scene }: { scene: AnimationPreviewScene }) {
  const { t } = useI18n();
  const fitRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const rangeRef = useRef<HTMLInputElement>(null);
  const labelRef = useRef<HTMLSpanElement>(null);
  const clock = useRef({ ms: 0, playing: true, apply: (_ms: number) => {} });
  const [playing, setPlaying] = useState(true);

  useEffect(() => {
    const fit = fitRef.current;
    const stage = stageRef.current;
    if (!fit || !stage) return;

    clock.current.ms = 0;
    clock.current.playing = true;
    setPlaying(true);

    const img = document.createElement('img');
    img.className = 'ppts-slide';
    img.alt = '';
    img.decoding = 'async';
    img.draggable = false;
    const imgTop = document.createElement('img');
    imgTop.className = 'ppts-slide';
    imgTop.alt = '';
    imgTop.decoding = 'async';
    imgTop.draggable = false;
    imgTop.hidden = true;
    const layersBox = document.createElement('div');
    layersBox.className = 'ppts-layers';
    stage.replaceChildren(img, imgTop, layersBox);

    const reduceMotion = false;
    const layers = (scene.layers ?? []) as ManifestLayer[];
    const focus = scene.focus ?? [];
    const annotations = scene.annotations ?? [];
    let compiled: CompiledLayer[] = [];
    if (needsLayerStage(layers, focus, annotations)) {
      compiled = buildLayerNodes(layersBox, layers, scene.width, scene.height, {
        fps: scene.fps > 0 ? scene.fps : 30,
        pageDurationMs: scene.durationMs,
        reduceMotion,
        focus,
        annotations,
        lookCrop: scene.lookCrop,
        pageImage: scene.image,
        clearRects: scene.clearRects
      });
    }

    const videoW = scene.width;
    const videoH = scene.height;
    let shotOnStage = false;

    const setShotSrc = (node: HTMLImageElement, path: string): void => {
      if (!path || node.dataset.shot === path) return;
      node.dataset.shot = path;
      node.src = path;
    };
    const hideTop = (): void => {
      imgTop.hidden = true;
      imgTop.style.opacity = '0';
      imgTop.removeAttribute('src');
      delete imgTop.dataset.shot;
    };
    const applyCamera = (node: HTMLImageElement, shot: Shot, localMs: number): void => {
      const cam = shot.camera;
      if (!cam || cam.length === 0) {
        node.style.transform = '';
        node.style.transformOrigin = '';
        return;
      }
      node.style.transformOrigin = '';
      const { x, y, scale } = cameraTransform(cam, localMs - shot.startMs);
      const tx = (0.5 - x) * videoW * scale;
      const ty = (0.5 - y) * videoH * scale;
      node.style.transform =
        `translate(calc(${tx}px * var(--ppts-k, 1)), calc(${ty}px * var(--ppts-k, 1))) scale(${scale})`;
    };
    const syncShot = (localMs: number): void => {
      const shots = scene.shots ?? [];
      const has = shots.length > 0;
      if (has !== shotOnStage) {
        shotOnStage = has;
        img.classList.toggle('ppts-shot', has);
        imgTop.classList.toggle('ppts-shot', has);
      }
      const frame = shotAt(shots, localMs);
      if (!frame) {
        setShotSrc(img, scene.image);
        hideTop();
        applyLookCrop(img, {
          lookCrop: scene.lookCrop,
          lookCropPadPx: scene.lookCropPadPx,
          reduceMotion,
          focus: scene.focus,
          tMs: localMs,
          canvasW: videoW,
          canvasH: videoH,
          layers: layersBox
        });
        layersBox.style.visibility = '';
        return;
      }
      const fading = frame.from !== undefined && frame.progress < 1;
      if (fading && frame.from) {
        setShotSrc(img, frame.from.image);
        setShotSrc(imgTop, frame.shot.image);
        imgTop.hidden = false;
        imgTop.style.opacity = String(frame.progress);
      } else {
        setShotSrc(img, frame.shot.image);
        hideTop();
      }
      applyLookCrop(img, {
        lookCrop: false,
        tMs: localMs,
        canvasW: videoW,
        canvasH: videoH,
        layers: layersBox
      });
      applyCamera(img, frame.from ?? frame.shot, localMs);
      if (fading && frame.from) applyCamera(imgTop, frame.shot, localMs);
      layersBox.style.visibility = frame.shot.crop ? 'hidden' : '';
    };

    const paintChrome = (ms: number): void => {
      if (rangeRef.current && document.activeElement !== rangeRef.current) {
        rangeRef.current.value = String(Math.round(ms));
      }
      if (labelRef.current) labelRef.current.textContent = formatClock(ms);
    };
    const apply = (ms: number): void => {
      syncShot(ms);
      for (const layer of compiled) seekLayer(layer, ms);
      paintChrome(ms);
    };
    clock.current.apply = apply;

    const fitSize = (): void => {
      const cw = fit.clientWidth;
      const ch = fit.clientHeight;
      const ar = videoW > 0 && videoH > 0 ? videoW / videoH : 16 / 9;
      if (cw > 0 && ch > 0) {
        let w = cw;
        let h = w / ar;
        if (h > ch) {
          h = ch;
          w = h * ar;
        }
        stage.style.width = `${w}px`;
        stage.style.height = `${h}px`;
        if (videoW > 0) stage.style.setProperty('--ppts-k', String(w / videoW));
        return;
      }
      stage.style.width = '100%';
      stage.style.height = '';
      stage.style.setProperty('--ppts-k', '1');
    };
    fitSize();
    const observer = new ResizeObserver(fitSize);
    observer.observe(fit);

    let raf = 0;
    let last = performance.now();
    const tick = (now: number): void => {
      const dt = now - last;
      last = now;
      if (clock.current.playing && scene.durationMs > 0) {
        let next = clock.current.ms + dt;
        if (next >= scene.durationMs) next %= scene.durationMs;
        clock.current.ms = next;
      }
      apply(clock.current.ms);
      raf = requestAnimationFrame(tick);
    };
    apply(0);
    raf = requestAnimationFrame(tick);

    return () => {
      cancelAnimationFrame(raf);
      observer.disconnect();
      releaseLayers(compiled);
      clock.current.apply = () => {};
    };
  }, [scene]);

  const toggle = (): void => {
    clock.current.playing = !clock.current.playing;
    setPlaying(clock.current.playing);
  };

  return (
    <div className="slide-stage-playback">
      <div className="browser-stage-fit" ref={fitRef}>
        <div
          className="browser-stage"
          ref={stageRef}
          style={{ aspectRatio: `${scene.width} / ${scene.height}` }}
        />
      </div>
      <div className="browser-stage-controls">
        <button type="button" className="button-ghost" onClick={toggle}>
          {playing ? t('editor.animationPreviewPause') : t('editor.animationPreviewPlay')}
        </button>
        <input
          ref={rangeRef}
          type="range"
          min={0}
          max={Math.max(1, scene.durationMs)}
          step={1}
          defaultValue={0}
          aria-label={t('editor.animationPreviewScrub')}
          onInput={(event) => {
            const ms = Number(event.currentTarget.value);
            if (!Number.isFinite(ms)) return;
            clock.current.ms = ms;
            clock.current.apply(ms);
          }}
        />
        <span ref={labelRef} className="browser-stage-time">0:00.0</span>
      </div>
    </div>
  );
}
