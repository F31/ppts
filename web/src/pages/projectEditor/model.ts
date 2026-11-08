import type { ScriptRevision, SlideSummary } from '../../types';

// i18n 的翻译函数签名。抽出来是为了让 projectEditor/ 下的模块不依赖 i18n 的上下文实现，
// 只依赖"能按 key 取文案"这一个能力（便于单测直接传入假 t）。
export type Translate = (key: string, vars?: Record<string, string | number>) => string;

// 一键成片可选的动效风格。与后端 catalog.json 的预设 id 一一对应；
// 文案写死成字面量是为了让 i18n 门禁能静态核对（动态拼 key 会漏检）。
export const ONE_CLICK_PRESETS = [
  { id: 'business-calm', label: 'editor.oneClickPresetCalm' },
  { id: 'tech-dynamic', label: 'editor.oneClickPresetTech' },
  { id: 'teaching-clear', label: 'editor.oneClickPresetTeaching' },
  { id: 'launch-bold', label: 'editor.oneClickPresetLaunch' }
] as const;

export type SlidesState =
  | { mode: 'loading' }
  // A26：empty 是"这个版本确实没有页面"，failed 是"页面列表没读到"。
  // 两者外观必须可区分，否则后端/网络故障会被当成"PPT 是空的"，且不给重试入口。
  // empty 可携带可选 message：默认显示 editor.noSlides；解析中明确显示 editor.parseRunning。
  | { mode: 'empty'; message?: string }
  | { mode: 'failed'; message: string }
  | { mode: 'real'; slides: SlideSummary[]; revisionNo: number };

// 编辑器内的"探测类"请求（版本列表/项目信息/配音状态/缩略图/讲稿来源/语音属性）。
// 这些请求失败后原先只是"保持空态"，界面与"确实没有"完全同貌（A26 禁止的形态）。
// 统一收进 probeErrors，并按渲染位置分两组：顶部失败带 / 语音抽屉内。
export type ProbeId =
  | 'revisions'
  | 'project'
  | 'narration'
  | 'thumbnails'
  | 'scriptSources'
  | 'voiceModels'
  | 'voiceSettings'
  | 'voiceStale';

// 顶部失败带覆盖的探测项（其余在语音抽屉内就地渲染）。
export const topProbeIds: ProbeId[] = ['revisions', 'project', 'narration', 'thumbnails', 'scriptSources'];

export type DraftStatus = { phase: 'idle' | 'generating' | 'ready' | 'error'; message: string };

export type ConflictState = { slideId: string; localText: string; latest: ScriptRevision } | null;

// B3-M5：生成类任务（配音生成 / 讲稿生成）的活跃态判定，用于"生成中继续编辑"顶部快照提示。
export const genJobKinds = ['narration', 'script_draft'];
export const genActiveStates = [
  'JOB_STATE_QUEUED',
  'JOB_STATE_RUNNING',
  'JOB_STATE_RETRY_WAIT',
  'JOB_STATE_CANCEL_REQUESTED',
  'JOB_STATE_UNKNOWN_PROVIDER_RESULT'
];
