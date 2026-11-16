// 配音任务在第一段失败时整单失败。worker 把页身份写进固定前缀，
// 界面据此显示「第 N 页」和供应商原文，并只重试这一页。
// 前缀保持英文，方便任务列表里的历史错误被同一种方式解析。

export type NarrationPageFailure = {
  slideId: string;
  page: number;
  segmentId: string;
  reason: string;
};

export type NarrationSlideRef = {
  slideId: string;
  title?: string;
};

const MARK = 'narration page failed: ';

export function parseNarrationPageFailure(message: string): NarrationPageFailure | null {
  if (!message) return null;
  const at = message.indexOf(MARK);
  if (at < 0) return null;
  const match = /^slideId=(\S+) page=(\d+) segment=(\S+): ([\s\S]+)$/.exec(message.slice(at + MARK.length));
  if (!match) return null;
  const page = Number(match[2]);
  if (!Number.isInteger(page) || page < 1) return null;
  const reason = match[4].trim();
  if (!reason) return null;
  return { slideId: match[1], page, segmentId: match[3], reason };
}

type Translate = (key: string, vars?: Record<string, string | number>) => string;

// narrationPageNumber 优先用当前文稿里的位置（与左侧页码一致，从 1 起）。
// 对不上时退回任务里记录的页序（有讲稿的页，跳过没讲稿的页）。
export function narrationPageNumber(failure: NarrationPageFailure, slides: NarrationSlideRef[]): number {
  const pos = slides.findIndex((slide) => slide.slideId === failure.slideId);
  return pos >= 0 ? pos + 1 : failure.page;
}

export function formatNarrationPageFailure(message: string, slides: NarrationSlideRef[], t: Translate): string {
  const failure = parseNarrationPageFailure(message);
  if (!failure) return message;
  const page = narrationPageNumber(failure, slides);
  const title = slides.find((slide) => slide.slideId === failure.slideId)?.title?.trim() ?? '';
  if (title) return t('editor.narrationPageFailedTitle', { page, title, reason: failure.reason });
  return t('editor.narrationPageFailed', { page, reason: failure.reason });
}
