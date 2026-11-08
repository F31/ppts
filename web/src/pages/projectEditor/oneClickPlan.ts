import type { ScriptRevision, SlideSummary } from '../../types';

export type OneDraftSource = 'notes_first' | 'page_content' | 'notes_only';
export type OneDraftOverwrite = 'fill_empty' | 'overwrite_all';
export type OneDraftLanguage = 'zh-CN' | 'en-US';

export type OneDraftPlan = {
  generate: number;
  skippedExisting: number;
  noText: number;
  withNotes: number;
  total: number;
  /** 跳过数是否精确：realScripts 只含当前讲稿语言的稿，语言不一致时只能算估算值。 */
  reliable: boolean;
};

/**
 * 成稿前的跳过预估（**估算**，非承诺值；后端逐页结果以任务步骤为准）。
 *
 * slide.preview 由后端按「备注优先，其次首个非空形状文字」生成：
 *  - notes_only：「有备注」即可成稿，preview 不作判据；
 *  - 其余来源：preview 为空 ⇒ 备注与形状文字皆空 ⇒ 必然跳过；
 *    但 preview 非空**不保证**页面文字可抽取（仅备注页在 page_content 下仍会被后端跳过），
 *    故这类漏计只会让预估偏乐观，不会漏报真正会生成的页。
 *
 * realScripts 只含**当前讲稿语言**的讲稿，故仅当生成语言与面板语言一致时跳过数才精确，
 * 否则标注为按目标语言的估算。
 *
 * withNotes 用于说明「打开有备注的页面会自动用备注建一版原文稿」——
 * 这会让「仅补空」在后续成稿时把这些页判为已有讲稿而跳过。
 */
export function computeOneDraftPlan(input: {
  slides: SlideSummary[];
  scripts: Record<string, ScriptRevision>;
  source: OneDraftSource;
  overwrite: OneDraftOverwrite;
  language: OneDraftLanguage;
  panelLanguage: string;
}): OneDraftPlan {
  let generate = 0;
  let skippedExisting = 0;
  let noText = 0;
  let withNotes = 0;
  for (const slide of input.slides) {
    if (slide.hasNotes) withNotes += 1;
    const hasMaterial =
      input.source === 'notes_only' ? Boolean(slide.hasNotes) : Boolean((slide.preview ?? '').trim());
    if (!hasMaterial) {
      noText += 1;
      continue;
    }
    const hasScript = (input.scripts[slide.slideId]?.segments.length ?? 0) > 0;
    if (hasScript && input.overwrite === 'fill_empty') {
      skippedExisting += 1;
      continue;
    }
    generate += 1;
  }
  return {
    generate,
    skippedExisting,
    noText,
    withNotes,
    total: input.slides.length,
    reliable: input.language === (input.panelLanguage || 'zh-CN')
  };
}
