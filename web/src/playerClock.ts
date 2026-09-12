import type { SlideCue, SubtitleCue, Timeline } from './types';

export function usecToClock(us: number): string {
  const totalMs = Math.max(0, Math.floor(us / 1000));
  const minutes = Math.floor(totalMs / 60_000);
  const seconds = Math.floor(totalMs / 1000) % 60;
  const ms = totalMs % 1000;
  return `${minutes}:${seconds.toString().padStart(2, '0')}.${Math.floor(ms / 100)}`;
}

export function slideAt(timeline: Timeline, positionUs: number): SlideCue {
  return timeline.slides.find((slide) => positionUs >= slide.startUs && positionUs < slide.endUs) ?? timeline.slides[timeline.slides.length - 1];
}

export function subtitleAt(timeline: Timeline, positionUs: number): SubtitleCue | undefined {
  return timeline.subtitles.find((cue) => positionUs >= cue.startUs && positionUs < cue.endUs);
}

export function progress(positionUs: number, durationUs: number): number {
  if (durationUs <= 0) return 0;
  return Math.min(1, Math.max(0, positionUs / durationUs));
}
