/**
 * slideVisibility —— 隐藏页的判定与作用域过滤。
 *
 * 隐藏页（`p:sldId@show="0"`）在放映与 PDF 导出里都会被跳过，但接口以前不返回这个事实，
 * 界面上和普通页长得一样。现在解析层把它写进 `featureFlags`（见 internal/project/reader.go），
 * 这里只做两件事：认这个标记，以及在"全篇"范围里默认把它排除。
 *
 * 为什么默认排除：用户点"全部重新生成"时，隐藏页既不会被播出、也不会进成片，
 * 给它配音纯属白花钱；而"纳入"应该是显式选择（勾选框，或直接选中该页单独生成）。
 */

export const HIDDEN_PAGE_FLAG = 'hidden';

export type SlideFlagSource = { featureFlags?: string[] };

export function isHiddenPage(slide: SlideFlagSource | null | undefined): boolean {
  if (!slide || !Array.isArray(slide.featureFlags)) return false;
  return slide.featureFlags.includes(HIDDEN_PAGE_FLAG);
}

/**
 * scopeSlideIds 按"是否纳入隐藏页"过滤作用域，保持传入顺序。
 *
 * 只过滤**能查到标记**的页：查不到的（例如幻灯片列表还没加载完）按可见处理，
 * 宁可多算一页，也不要因为列表缺项而把用户的页悄悄漏掉。
 */
export function scopeSlideIds<T extends SlideFlagSource>(
  slides: T[],
  ids: string[],
  slideIdOf: (slide: T) => string,
  includeHidden: boolean
): string[] {
  if (includeHidden) return ids;
  const hidden = new Set(slides.filter(isHiddenPage).map(slideIdOf));
  return ids.filter((id) => !hidden.has(id));
}

/** 供界面显示用：全篇里有几页是隐藏页。 */
export function hiddenCount(slides: SlideFlagSource[]): number {
  return slides.filter(isHiddenPage).length;
}
