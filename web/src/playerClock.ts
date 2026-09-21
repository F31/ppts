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

// 当前已朗读到的字符下标（-1 = 尚未开始，text.length = 已读完全部）。
// 依赖 cue.chars 字符级时间戳；缺失时按整段线性估算。
export function spokenCharAt(cue: SubtitleCue, positionUs: number): number {
  const chars = cue.chars;
  if (!chars || chars.length === 0) {
    if (cue.endUs <= cue.startUs) return -1;
    const ratio = (positionUs - cue.startUs) / (cue.endUs - cue.startUs);
    return Math.floor(Math.max(0, Math.min(1, ratio)) * cue.text.length);
  }
  // chars 下标与 text 下标一一对应；取最后一个 startUs <= positionUs 的字符。
  let idx = -1;
  for (let i = 0; i < chars.length; i++) {
    if (chars[i].startUs <= positionUs) idx = i;
    else break;
  }
  return idx;
}

const SUBTITLE_LINE_MIN = 12;
const SUBTITLE_LINE_MAX = 34;
const SUBTITLE_BREAK_CHARS = new Set(['。', '！', '？', '；', '，', '、', '.', '!', '?', ';', ',']);

// 把字幕文本切成单行轮换片段，返回每行对应的 [startChar, endChar) 区间。
// 优先尊重换行；无换行的整段讲稿按标点与长度拆分，避免字幕条一次铺满多行。
export function splitLines(cue: SubtitleCue): { start: number; end: number }[] {
  const out: { start: number; end: number }[] = [];

  const trimRange = (start: number, end: number): { start: number; end: number } | null => {
    while (start < end && /\s/.test(cue.text[start])) start++;
    while (end > start && /\s/.test(cue.text[end - 1])) end--;
    return start < end ? { start, end } : null;
  };

  const pushWrapped = (start: number, end: number) => {
    let cursor = start;
    while (cursor < end) {
      const hardLimit = Math.min(end, cursor + SUBTITLE_LINE_MAX);
      let next = hardLimit;
      if (hardLimit < end) {
        let breakAt = -1;
        for (let i = cursor + SUBTITLE_LINE_MIN; i <= hardLimit; i++) {
          if (SUBTITLE_BREAK_CHARS.has(cue.text[i - 1])) breakAt = i;
        }
        if (breakAt < 0) {
          for (let i = hardLimit; i > cursor + SUBTITLE_LINE_MIN; i--) {
            if (/\s/.test(cue.text[i - 1])) {
              breakAt = i;
              break;
            }
          }
        }
        if (breakAt > cursor) next = breakAt;
      }
      const range = trimRange(cursor, next);
      if (range) out.push(range);
      cursor = next;
    }
  };

  let start = 0;
  for (let i = 0; i <= cue.text.length; i++) {
    if (i === cue.text.length || cue.text[i] === '\n') {
      pushWrapped(start, i);
      start = i + 1;
    }
  }
  return out.length > 0 ? out : [{ start: 0, end: 0 }];
}

export function progress(positionUs: number, durationUs: number): number {
  if (durationUs <= 0) return 0;
  return Math.min(1, Math.max(0, positionUs / durationUs));
}
