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
// 依赖 cue.chars 字符级时间戳；缺失时按行均分（与后端 media.SplitSubtitleCues / ASS 的
// distributedLine 同口径），避免网页整段线性、烧录按行均分两套节奏对不上。
export function spokenCharAt(cue: SubtitleCue, positionUs: number): number {
  const chars = cue.chars;
  if (!chars || chars.length === 0) {
    return spokenCharFallback(cue, positionUs);
  }
  // chars 下标与 text 下标一一对应；取最后一个 startUs <= positionUs 的字符。
  // 长字幕可达数十字，用二分替代线性扫描（每帧渲染都会调用）。
  let lo = 0;
  let hi = chars.length - 1;
  let idx = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (chars[mid].startUs <= positionUs) {
      idx = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
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

// Chars 缺失时的高亮估算：与后端 media.SplitSubtitleCues / RenderASS 的 distributedLine
// 同口径——先把 cue 时长按行数均分，定位当前行，再在该行的字符区间内线性推进。
// 旧实现按整段线性，与 ASS 烧录的按行均分在断行处节奏不同，导致"预览和成片高亮对不上"。
function spokenCharFallback(cue: SubtitleCue, positionUs: number): number {
  const dur = cue.endUs - cue.startUs;
  if (dur <= 0) return -1;
  if (positionUs <= cue.startUs) return -1;
  if (positionUs >= cue.endUs) return cue.text.length;
  const lines = splitLines(cue);
  if (lines.length === 0 || (lines.length === 1 && lines[0].end <= lines[0].start)) {
    const ratio = (positionUs - cue.startUs) / dur;
    return Math.floor(Math.max(0, Math.min(1, ratio)) * cue.text.length);
  }
  const perLine = dur / lines.length;
  for (let i = 0; i < lines.length; i++) {
    const lineStart = cue.startUs + perLine * i;
    const lineEnd = cue.startUs + perLine * (i + 1);
    if (positionUs < lineEnd || i === lines.length - 1) {
      const { start, end } = lines[i];
      if (end <= start) return start;
      const frac = Math.max(0, Math.min(1, (positionUs - lineStart) / (lineEnd - lineStart)));
      return start + Math.floor(frac * (end - start));
    }
  }
  return cue.text.length;
}
