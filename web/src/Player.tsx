import { useEffect, useMemo, useRef, useState } from 'react';
import { useI18n } from './i18n';
import type { PlaybackManifest, Timeline } from './types';
import { progress, slideAt, subtitleAt, usecToClock } from './playerClock';

type PlayerProps = {
  manifest: PlaybackManifest;
  onSlideChange?: (slideId: string) => void;
};

export function Player({ manifest, onSlideChange }: PlayerProps) {
  const { t } = useI18n();
  const timeline = useMemo(() => JSON.parse(manifest.timelineJson) as Timeline, [manifest.timelineJson]);
  const [playing, setPlaying] = useState(false);
  const [positionUs, setPositionUs] = useState(0);
  const startWall = useRef(0);
  const startPosition = useRef(0);

  useEffect(() => {
    if (!playing) return;
    let raf = 0;
    startWall.current = performance.now();
    startPosition.current = positionUs;
    const tick = () => {
      const elapsedUs = (performance.now() - startWall.current) * 1000;
      const next = Math.min(timeline.durationUs, startPosition.current + elapsedUs);
      setPositionUs(next);
      if (next < timeline.durationUs) {
        raf = requestAnimationFrame(tick);
      } else {
        setPlaying(false);
      }
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [playing, positionUs, timeline.durationUs]);

  const activeSlide = slideAt(timeline, positionUs);
  const activeSubtitle = subtitleAt(timeline, positionUs);
  const pageResource = manifest.resources.find((resource) => resource.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG' && resource.slideId === activeSlide.slideId);

  useEffect(() => {
    onSlideChange?.(activeSlide.slideId);
  }, [activeSlide.slideId, onSlideChange]);

  const seek = (value: number) => {
    setPositionUs(Math.round(timeline.durationUs * value));
    setPlaying(false);
  };

  return (
    <section className="player-card" aria-label={t('player.aria')}>
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
      </div>
      <div className="player-controls">
        <button type="button" onClick={() => setPlaying((value) => !value)}>{playing ? t('player.pause') : t('player.play')}</button>
        <button type="button" onClick={() => setPositionUs(0)}>{t('player.restart')}</button>
        <span>{usecToClock(positionUs)} / {usecToClock(timeline.durationUs)}</span>
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
    </section>
  );
}
