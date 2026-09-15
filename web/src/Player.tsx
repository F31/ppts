import { useEffect, useMemo, useRef, useState } from 'react';
import { useI18n } from './i18n';
import type { PlaybackManifest, PlaybackResource, Timeline } from './types';
import { progress, slideAt, subtitleAt, usecToClock } from './playerClock';

type PlayerProps = {
  manifest: PlaybackManifest;
  onSlideChange?: (slideId: string) => void;
};

const SPEEDS = [0.5, 1, 1.25, 1.5, 2];

type AudioSegment = { startUs: number; endUs: number; url: string };

export function Player({ manifest, onSlideChange }: PlayerProps) {
  const { t } = useI18n();
  const timeline = useMemo(() => JSON.parse(manifest.timelineJson) as Timeline, [manifest.timelineJson]);

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
  const [positionUs, setPositionUs] = useState(0);
  const [speed, setSpeed] = useState(1);
  const [buffering, setBuffering] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);

  const audioRef = useRef<HTMLAudioElement | null>(null);
  const cardRef = useRef<HTMLDivElement | null>(null);
  const currentIndexRef = useRef(-1);
  const seekingRef = useRef(false);

  const activeSlide = slideAt(timeline, positionUs);
  const activeSubtitle = subtitleAt(timeline, positionUs);
  const pageResource = manifest.resources.find(
    (r: PlaybackResource) => r.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG' && r.slideId === activeSlide.slideId
  );

  useEffect(() => {
    onSlideChange?.(activeSlide.slideId);
  }, [activeSlide.slideId, onSlideChange]);

  // 切换 manifest（新快照）时复位播放状态，避免串音。
  useEffect(() => {
    setPlaying(false);
    setPositionUs(0);
    setBuffering(false);
    currentIndexRef.current = -1;
    seekingRef.current = false;
  }, [manifest.timelineKey]);

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
    const onFsChange = () => setFullscreen(Boolean(document.fullscreenElement));
    document.addEventListener('fullscreenchange', onFsChange);
    return () => document.removeEventListener('fullscreenchange', onFsChange);
  }, []);

  const seek = (value: number) => {
    const target = Math.round(timeline.durationUs * value);
    if (hasAudio) seekingRef.current = true;
    setPositionUs(target);
    setPlaying(false);
  };

  const toggleFullscreen = () => {
    const el = cardRef.current;
    if (!el) return;
    if (document.fullscreenElement) {
      void document.exitFullscreen().catch(() => {});
    } else {
      void el.requestFullscreen?.().catch(() => {});
    }
  };

  return (
    <section
      className={`player-card${fullscreen ? ' is-fullscreen' : ''}`}
      aria-label={t('player.aria')}
      ref={cardRef}
    >
      <audio ref={audioRef} key={manifest.timelineKey} preload="auto" />
      <div className="viewport">
        {pageResource?.signedUrl ? (
          <img src={pageResource.signedUrl} alt={activeSlide.slideId} />
        ) : (
          <div className="slide-fallback">
            <span>{activeSlide.slideId}</span>
            <strong>{activeSubtitle?.text ?? t('player.waitingSubtitle')}</strong>
          </div>
        )}
        <div className="subtitle-strip">{activeSubtitle?.text ?? ' '}</div>
        {buffering && <div className="player-buffering">{t('player.buffering')}</div>}
      </div>
      <div className="player-controls">
        <button type="button" onClick={() => setPlaying((value) => !value)}>
          {playing ? t('player.pause') : t('player.play')}
        </button>
        <button type="button" onClick={() => seek(0)}>
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
      </div>
      <input
        className="timeline-range"
        type="range"
        min={0}
        max={1000}
        value={Math.round(progress(positionUs, timeline.durationUs) * 1000)}
        onChange={(event) => seek(Number(event.currentTarget.value) / 1000)}
        aria-label={t('player.progress')}
      />
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
      {!hasAudio && <p className="player-note">{t('player.noAudio')}</p>}
    </section>
  );
}
