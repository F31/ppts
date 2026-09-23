import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useI18n } from './i18n';
import type { PlaybackManifest, PlaybackResource, Timeline } from './types';
import { progress, slideAt, spokenCharAt, splitLines, subtitleAt, usecToClock } from './playerClock';

type PlayerProps = {
  manifest: PlaybackManifest;
  activeSlideId?: string;
  activeSlideIndex?: number;
  activeImageUrl?: string;
  activeImageLabel?: string;
  embedded?: boolean;
  pauseSignal?: number;
  onSlideChange?: (slideId: string) => void;
};

const SPEEDS = [0.5, 1, 1.25, 1.5, 2];

type AudioSegment = { startUs: number; endUs: number; url: string };

export function Player({ manifest, activeSlideId, activeSlideIndex = -1, activeImageUrl, activeImageLabel, embedded = false, pauseSignal = 0, onSlideChange }: PlayerProps) {
  const { t } = useI18n();
  const timeline = useMemo(() => JSON.parse(manifest.timelineJson) as Timeline, [manifest.timelineJson]);
  const requestedTimelineSlide = useMemo(() => {
    if (!activeSlideId) return undefined;
    return timeline.slides.find((slide) => slide.slideId === activeSlideId) ?? (activeSlideIndex >= 0 ? timeline.slides[activeSlideIndex] : undefined);
  }, [timeline.slides, activeSlideId, activeSlideIndex]);
  const initialPositionUs = requestedTimelineSlide?.startUs ?? 0;

  // 将 manifest 中的 AUDIO 资源与 timeline 段按 (slideId#segmentId) / audioKey 关联，按时间排序。
  const audioSegments = useMemo<AudioSegment[]>(() => {
    const urlBySeg = new Map<string, string>();
    const urlByKey = new Map<string, string>();
    for (const r of manifest.resources) {
      if (r.type !== 'PLAYBACK_RESOURCE_TYPE_AUDIO') continue;
      if (r.slideId && r.segmentId) urlBySeg.set(`${r.slideId}#${r.segmentId}`, r.signedUrl);
      if (r.key) urlByKey.set(r.key, r.signedUrl);
    }
    const segs: AudioSegment[] = [];
    for (const slide of timeline.slides) {
      for (const seg of slide.segments) {
        const url = urlBySeg.get(`${slide.slideId}#${seg.segmentId}`) ?? urlByKey.get(seg.audioKey);
        if (url) segs.push({ startUs: seg.startUs, endUs: seg.endUs, url });
      }
    }
    segs.sort((a, b) => a.startUs - b.startUs);
    return segs;
  }, [timeline, manifest.resources]);

  const hasAudio = audioSegments.length > 0;

  const [playing, setPlaying] = useState(false);
  const [positionUs, setPositionUs] = useState(initialPositionUs);
  const [speed, setSpeed] = useState(1);
  const [buffering, setBuffering] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [controlsVisible, setControlsVisible] = useState(true);
  const [subtitlesEnabled, setSubtitlesEnabled] = useState(true);

  const audioRef = useRef<HTMLAudioElement | null>(null);
  const cardRef = useRef<HTMLDivElement | null>(null);
  const currentIndexRef = useRef(-1);
  const seekingRef = useRef(false);
  // 上一次由父级传入的 activeSlideId；用于区分"父级换页"与"播放位置跨页"。
  const lastRequestedSlideIdRef = useRef<string | undefined>(undefined);
  const hideControlsTimerRef = useRef<number | null>(null);

  const activeSlide = slideAt(timeline, positionUs);
  const activeSubtitle = subtitleAt(timeline, positionUs);
  const pageResource = manifest.resources.find(
    (r: PlaybackResource) => r.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG' && r.slideId === activeSlide.slideId
  );
  const showingExternalSlide = Boolean(activeSlideId && !requestedTimelineSlide && activeImageUrl);
  const displayImageUrl = showingExternalSlide ? activeImageUrl : (pageResource?.signedUrl ?? activeImageUrl);
  const displayImageAlt = showingExternalSlide || !pageResource ? (activeImageLabel ?? activeSlideId ?? activeSlide.slideId) : activeSlide.slideId;
  const selectedSlideHasAudio = !activeSlideId || Boolean(requestedTimelineSlide);
  const displaySubtitle = showingExternalSlide ? null : activeSubtitle;

  // B4-M6 字幕：只显示当前朗读的那一行（按 \n 切行、随朗读位置轮换），已朗读字符用高亮色。
  const spokenSubtitle = (() => {
    if (!displaySubtitle) return null;
    const at = spokenCharAt(displaySubtitle, positionUs);
    const lines = splitLines(displaySubtitle);
    const found = lines.findIndex((line) => at >= line.start && at < line.end);
    const lineIdx = found >= 0 ? found : at >= displaySubtitle.text.length ? lines.length - 1 : 0;
    const line = lines[lineIdx];
    const spokenInLine = Math.max(0, Math.min(at, line.end) - line.start);
    const before = displaySubtitle.text.slice(line.start, line.start + spokenInLine);
    const after = displaySubtitle.text.slice(line.start + spokenInLine, line.end);
    return { before, after };
  })();

  // 仅在播放进度跨越到不同幻灯片时才通知父组件切页。
  // 不能把 onSlideChange 放进依赖并在每次渲染触发：父组件传入的回调每次渲染都是新引用，
  // 会把手动点缩略图切到的页立刻被覆盖回播放器当前页（有配音的项目才挂载播放器，故会有项目差异）。
  const onSlideChangeRef = useRef(onSlideChange);
  onSlideChangeRef.current = onSlideChange;
  useEffect(() => {
    onSlideChangeRef.current?.(activeSlide.slideId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeSlide.slideId]);

  useLayoutEffect(() => {
    // 只在父级"显式换了页"（activeSlideId 变化）时才回流 seek。
    // 播放/拖动进度导致的跨页由 onSlideChange 上报父级，位置是权威来源；
    // 若这里也按父级 activeSlideId 反向吸附，两者会互相拉扯形成无限更新（React #185）。
    const changedByParent = lastRequestedSlideIdRef.current !== activeSlideId;
    lastRequestedSlideIdRef.current = activeSlideId;
    if (!activeSlideId) return;
    if (!requestedTimelineSlide) {
      if (changedByParent) {
        setPlaying(false);
        currentIndexRef.current = -1;
      }
      return;
    }
    if (!changedByParent) return;
    if (requestedTimelineSlide.slideId === activeSlide.slideId) return;
    if (hasAudio) seekingRef.current = true;
    currentIndexRef.current = -1;
    setPositionUs(requestedTimelineSlide.startUs);
  }, [activeSlideId, activeSlide.slideId, requestedTimelineSlide, hasAudio]);

  useEffect(() => {
    if (pauseSignal > 0) setPlaying(false);
  }, [pauseSignal]);

  // 切换 manifest（新快照）时复位播放状态，避免串音。
  useEffect(() => {
    setPlaying(false);
    setPositionUs(timeline.slides.find((slide) => slide.slideId === activeSlideId)?.startUs ?? 0);
    setBuffering(false);
    currentIndexRef.current = -1;
    seekingRef.current = false;
  }, [manifest.timelineKey, timeline.slides]);

  // 段定位：返回最后一个 startUs <= positionUs 的段索引
  const indexForPosition = (posUs: number): number => {
    let idx = -1;
    for (let i = 0; i < audioSegments.length; i++) {
      if (audioSegments[i].startUs <= posUs) idx = i;
      else break;
    }
    return idx;
  };

  // 同步 <audio> 到当前位置 / 播放状态 / 倍速
  useEffect(() => {
    const audio = audioRef.current;
    if (!hasAudio || !audio) return;
    const idx = indexForPosition(positionUs);
    if (idx < 0) {
      audio.pause();
      return;
    }
    const seg = audioSegments[idx];
    const indexChanged = currentIndexRef.current !== idx;
    if (indexChanged) {
      audio.src = seg.url;
      currentIndexRef.current = idx;
      audio.playbackRate = speed;
    }
    if (indexChanged || seekingRef.current) {
      const offsetUs = Math.max(0, positionUs - seg.startUs);
      const applyOffset = () => {
        try {
          audio.currentTime = offsetUs / 1e6;
        } catch {
          /* 元数据未就绪时忽略 */
        }
        seekingRef.current = false;
      };
      if (audio.readyState >= 1) applyOffset();
      else audio.addEventListener('loadedmetadata', applyOffset, { once: true });
    }
    if (playing) {
      void audio.play().catch(() => {});
    } else {
      audio.pause();
    }
  }, [positionUs, playing, hasAudio, audioSegments, speed]);

  // 主时钟：有音频时以 audio.currentTime 为准（替代原合成时钟）；无音频回退到 rAF 墙钟（原行为）。
  useEffect(() => {
    if (!playing) return;
    let raf = 0;
    const startWall = performance.now();
    const startPosition = positionUs;
    const tick = () => {
      const audio = audioRef.current;
      const idx = currentIndexRef.current;
      if (hasAudio && idx >= 0 && idx < audioSegments.length && audio) {
        const seg = audioSegments[idx];
        const localUs = audio.currentTime * 1e6;
        const pos = seg.startUs + Math.min(localUs, seg.endUs - seg.startUs);
        setPositionUs(pos);
        if (pos >= timeline.durationUs) {
          setPlaying(false);
          return;
        }
      } else if (hasAudio && idx < 0 && audioSegments.length > 0 && positionUs < audioSegments[0].startUs) {
        // 段前间隙：墙钟推进到第一段起点，之后由音频接管
        const elapsedUs = (performance.now() - startWall) * 1000 * speed;
        const nextPos = Math.min(audioSegments[0].startUs, startPosition + elapsedUs);
        setPositionUs(nextPos);
      } else {
        const elapsedUs = (performance.now() - startWall) * 1000 * speed;
        const next = Math.min(timeline.durationUs, startPosition + elapsedUs);
        setPositionUs(next);
        if (next >= timeline.durationUs) {
          setPlaying(false);
          return;
        }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
    // 故意不含 positionUs：避免每帧重启导致墙钟分支失效
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playing, hasAudio, audioSegments, timeline.durationUs, speed]);

  // 段结束自动续播下一段；缓冲/播放状态用于指示
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const onEnded = () => {
      const next = currentIndexRef.current + 1;
      if (next < audioSegments.length) {
        currentIndexRef.current = -1; // 强制 sync effect 重新加载下一段
        setPositionUs(audioSegments[next].startUs);
        setPlaying(true);
      } else {
        setPlaying(false);
      }
    };
    const onWaiting = () => setBuffering(true);
    const onPlaying = () => setBuffering(false);
    const onCanPlay = () => setBuffering(false);
    audio.addEventListener('ended', onEnded);
    audio.addEventListener('waiting', onWaiting);
    audio.addEventListener('playing', onPlaying);
    audio.addEventListener('canplay', onCanPlay);
    return () => {
      audio.removeEventListener('ended', onEnded);
      audio.removeEventListener('waiting', onWaiting);
      audio.removeEventListener('playing', onPlaying);
      audio.removeEventListener('canplay', onCanPlay);
    };
  }, [audioSegments]);

  // 全屏状态同步
  useEffect(() => {
    const onFsChange = () => {
      const active = document.fullscreenElement === cardRef.current;
      setFullscreen(active);
      setControlsVisible(true);
    };
    document.addEventListener('fullscreenchange', onFsChange);
    return () => document.removeEventListener('fullscreenchange', onFsChange);
  }, []);

  const showFullscreenControls = () => {
    if (!fullscreen) return;
    if (hideControlsTimerRef.current !== null) window.clearTimeout(hideControlsTimerRef.current);
    setControlsVisible(true);
    hideControlsTimerRef.current = window.setTimeout(() => setControlsVisible(false), 2200);
  };

  useEffect(() => {
    if (hideControlsTimerRef.current !== null) window.clearTimeout(hideControlsTimerRef.current);
    if (!fullscreen) {
      setControlsVisible(true);
      return;
    }
    setControlsVisible(true);
    hideControlsTimerRef.current = window.setTimeout(() => setControlsVisible(false), 2200);
    return () => {
      if (hideControlsTimerRef.current !== null) window.clearTimeout(hideControlsTimerRef.current);
    };
  }, [fullscreen]);

  const seek = (value: number) => {
    if (!selectedSlideHasAudio) return;
    const target = Math.round(timeline.durationUs * value);
    if (hasAudio) seekingRef.current = true;
    setPositionUs(target);
    setPlaying(false);
  };

  // 翻页：跳到下一页/上一页的时间轴起点，并同步通知父级切换当前页。
  // 与 seek 不同，这里不依赖 selectedSlideHasAudio——只要时间轴里有该页就允许翻页。
  const currentSlideIndex = timeline.slides.findIndex((slide) => slide.slideId === activeSlide.slideId);
  const stepSlide = (delta: number) => {
    const next = timeline.slides[currentSlideIndex + delta];
    if (!next) return;
    if (hasAudio) seekingRef.current = true;
    currentIndexRef.current = -1;
    setPositionUs(next.startUs);
    onSlideChangeRef.current?.(next.slideId);
  };
  const canPrevSlide = currentSlideIndex > 0;
  const canNextSlide = currentSlideIndex >= 0 && currentSlideIndex < timeline.slides.length - 1;

  const toggleFullscreen = () => {
    const el = cardRef.current;
    if (!el) return;
    if (document.fullscreenElement) {
      void document.exitFullscreen().catch(() => {});
    } else {
      void el.requestFullscreen?.().then(() => {
        (document.activeElement as HTMLElement | null)?.blur?.();
      }).catch(() => {});
    }
  };

  // B4-M4（A25）：空格/方向键控制播放。
  // **不抢占输入**：焦点位于 input（含进度条滑块）/ select / textarea / button / a /
  // contenteditable 时一律让位——这样方向键在文本里正常移动光标、空格正常激活聚焦的按钮，
  // 只有焦点落在普通容器（如正文）上时才由播放器接管。
  // 用 latest-ref 承载最新状态，保证监听器只挂一次（避免每帧 rAF 更新都重挂）。
  const keyHandlerRef = useRef<(event: KeyboardEvent) => void>(() => {});
  keyHandlerRef.current = (event) => {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
    const active = document.activeElement as HTMLElement | null;
    const tag = active ? active.tagName.toLowerCase() : '';
    // 文本/下拉等输入控件完全让位；按钮/链接只让空格（避免误触聚焦的按钮），方向键仍归播放器。
    const isTextInput = tag === 'input' || tag === 'select' || tag === 'textarea' || Boolean(active?.isContentEditable);
    switch (event.key) {
      case ' ':
      case 'Spacebar':
        if (isTextInput || tag === 'button' || tag === 'a') return;
        event.preventDefault();
        if (selectedSlideHasAudio) setPlaying((value) => !value);
        return;
      case 'ArrowLeft':
      case 'ArrowRight':
      case 'ArrowUp':
      case 'ArrowDown': {
        if (isTextInput) return;
        // 内嵌在编辑器（ProjectEditor）时，方向键翻页由父级基于真实页面列表接管
        // （缩略图/阅览区/讲稿栏联动，全屏下依然生效），这里不能重复执行以免一次按键翻两页。
        if (embedded) return;
        // 方向键翻页（全屏/独立页一致）：左右与上下都切上一页/下一页。
        event.preventDefault();
        const delta = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1;
        stepSlide(delta);
        return;
      }
      default:
        return;
    }
  };

  useEffect(() => {
    const listener = (event: KeyboardEvent) => keyHandlerRef.current(event);
    window.addEventListener('keydown', listener);
    return () => window.removeEventListener('keydown', listener);
  }, []);

  return (
    <section
      className={`player-card${embedded ? ' embedded-player' : ''}${fullscreen ? ' is-fullscreen' : ''}${fullscreen && controlsVisible ? ' controls-visible' : ''}`}
      aria-label={t('player.aria')}
      ref={cardRef}
      onMouseMove={showFullscreenControls}
      onPointerMove={showFullscreenControls}
    >
      <audio ref={audioRef} key={manifest.timelineKey} preload="auto" />
      <div className="viewport">
        {displayImageUrl ? (
          <img src={displayImageUrl} alt={displayImageAlt} />
        ) : (
          <div className="slide-fallback">
            <span>{activeSlide.slideId}</span>
            <strong>{displaySubtitle?.text ?? t('player.waitingSubtitle')}</strong>
          </div>
        )}
        {subtitlesEnabled && (
          <div className="subtitle-strip">
            {spokenSubtitle ? (
              <span className="subtitle-line">
                {spokenSubtitle.before && <span className="subtitle-spoken">{spokenSubtitle.before}</span>}
                <span>{spokenSubtitle.after}</span>
              </span>
            ) : (
              ' '
            )}
          </div>
        )}
        {buffering && <div className="player-buffering">{t('player.buffering')}</div>}
      </div>
      {/* B4-M4（A25）：控制条 + 进度条收进 .player-dock，移动端吸附到视口底部（<768px）。 */}
      <div className="player-dock" aria-label={t('player.dock')} onFocus={showFullscreenControls} onMouseEnter={showFullscreenControls}>
        <div className="player-controls">
          <button
            type="button"
            className="player-step"
            disabled={!canPrevSlide}
            title={t('player.prevSlide')}
            aria-label={t('player.prevSlide')}
            onClick={() => stepSlide(-1)}
          >
            ‹
          </button>
          <button
            type="button"
            className="player-step"
            disabled={!canNextSlide}
            title={t('player.nextSlide')}
            aria-label={t('player.nextSlide')}
            onClick={() => stepSlide(1)}
          >
            ›
          </button>
          <button type="button" disabled={!selectedSlideHasAudio} onClick={() => setPlaying((value) => !value)}>
            {playing ? t('player.pause') : t('player.play')}
          </button>
          <button type="button" disabled={!selectedSlideHasAudio} onClick={() => seek(0)}>
            {t('player.restart')}
          </button>
          <span>
            {usecToClock(positionUs)} / {usecToClock(timeline.durationUs)}
          </span>
          {hasAudio && (
            <label className="player-speed">
              <span>{t('player.speed')}</span>
              <select
                value={String(speed)}
                onChange={(event) => setSpeed(Number(event.currentTarget.value))}
              >
                {SPEEDS.map((s) => (
                  <option key={String(s)} value={String(s)}>
                    {s}×
                  </option>
                ))}
              </select>
            </label>
          )}
          <button type="button" className="player-fullscreen" onClick={toggleFullscreen}>
            {fullscreen ? t('player.exitFullscreen') : t('player.fullscreen')}
          </button>
          <button
            type="button"
            className="player-cc"
            aria-pressed={subtitlesEnabled}
            title={subtitlesEnabled ? t('player.subtitlesOff') : t('player.subtitlesOn')}
            onClick={() => setSubtitlesEnabled((value) => !value)}
          >
            CC
          </button>
        </div>
        <input
          className="timeline-range"
          type="range"
          min={0}
          max={1000}
          value={Math.round(progress(positionUs, timeline.durationUs) * 1000)}
          onChange={(event) => seek(Number(event.currentTarget.value) / 1000)}
          aria-label={t('player.progress')}
          disabled={!selectedSlideHasAudio}
        />
      </div>
      {!embedded && (
        <div className="slide-map">
          {timeline.slides.map((slide) => (
            <button
              key={slide.slideId}
              type="button"
              className={slide.slideId === activeSlide.slideId ? 'active' : ''}
              style={{ width: `${Math.max(8, progress(slide.endUs - slide.startUs, timeline.durationUs) * 100)}%` }}
              onClick={() => seek(progress(slide.startUs, timeline.durationUs))}
            >
              {slide.slideId.replace('slide-', '')}
            </button>
          ))}
        </div>
      )}
      {!embedded && <p className="player-shortcuts">{t('player.shortcutsHint')}</p>}
      {(!hasAudio || !selectedSlideHasAudio) && <p className="player-note">{t('player.noAudio')}</p>}
    </section>
  );
}
