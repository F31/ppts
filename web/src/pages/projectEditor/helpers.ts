import type { Job, PlaybackManifest, ScriptMode, SlideSummary } from '../../types';

// 模式选项与标签：编辑器多处复用（模式选择下拉 + 生成状态文案）。
export const scriptModeOptions: Array<{ value: ScriptMode; labelKey: string; descKey: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', labelKey: 'editor.modes.original', descKey: 'editor.modes.originalDesc' },
  { value: 'SCRIPT_MODE_POLISH', labelKey: 'editor.modes.polish', descKey: 'editor.modes.polishDesc' },
  { value: 'SCRIPT_MODE_AI_GENERATED', labelKey: 'editor.modes.ai', descKey: 'editor.modes.aiDesc' }
];

export const modeLabel = (mode: ScriptMode | undefined, t: (key: string) => string) =>
  t(scriptModeOptions.find((item) => item.value === mode)?.labelKey ?? 'editor.modes.original');

// 播放清单时间线与当前页集对齐检查：任一页缺 slideId 或不在页集内即视为不匹配。
export function manifestMatchesSlides(manifest: PlaybackManifest, slides: SlideSummary[]): boolean {
  const allowed = new Set(slides.map((slide) => slide.slideId));
  try {
    const timeline = JSON.parse(manifest.timelineJson) as { slides?: Array<{ slideId?: string }> };
    return (timeline.slides ?? []).every((slide) => Boolean(slide.slideId && allowed.has(slide.slideId)));
  } catch {
    return false;
  }
}

// jobRevisionNo 从任务快照里取源版本号（配音任务快照含 revisionNo）。取不到返回 0。
// 用于把"正在生成语音"的状态限定到当前展示的版本，避免别的版本在生成时影响本版本的讲稿栏。
export function jobRevisionNo(job: Job): number {
  try {
    const snap = JSON.parse(job.inputSnapshot) as { revisionNo?: number | string };
    return Number(snap.revisionNo) || 0;
  } catch {
    return 0;
  }
}

export const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));
