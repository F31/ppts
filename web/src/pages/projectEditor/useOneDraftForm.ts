import { useState } from 'react';
import type { ScriptMode } from '../../types';
import type { OneDraftLanguage, OneDraftOverwrite, OneDraftSource } from './oneClickPlan';

/** 成稿表单：8 项配置（默认 0 配置即可提交，其余收在「高级设置」里）。 */
export type OneDraftForm = {
  language: OneDraftLanguage;
  source: OneDraftSource;
  mode: ScriptMode;
  length: 'brief' | 'standard' | 'detailed';
  style: string;
  audience: string;
  overwrite: OneDraftOverwrite;
};

// 语言偏好与讲稿栏共用同一个 localStorage 键：成稿选英文后，讲稿栏也切到英文。
const languageStorageKey = (projectId: string) => `ppts:script-language:${projectId}`;

function initialLanguage(projectId: string): OneDraftLanguage {
  try {
    return window.localStorage.getItem(languageStorageKey(projectId)) === 'en-US' ? 'en-US' : 'zh-CN';
  } catch {
    return 'zh-CN';
  }
}

// style / audience 的取值是**发给后端的领域值**（不是界面文案），界面文案由 option 标签各自本地化。
// 因此这里保留中文字面量，改动需与后端提示词词表同步。
const defaultStyle = '专业正式';
const defaultAudience = '通用听众';

/**
 * 成稿表单状态。
 *
 * 集中成一份对象的原因是：这份配置同时被「一键成片」与「单独成稿」两个入口读取，
 * 各存一份 state 会让两条路径读到不同的值（真机踩过：一键成片改了覆盖策略，
 * 另一个抽屉仍是默认的「仅补空」）。
 */
export function useOneDraftForm(projectId: string) {
  const [form, setForm] = useState<OneDraftForm>(() => ({
    language: initialLanguage(projectId),
    source: 'notes_first',
    mode: 'SCRIPT_MODE_POLISH',
    length: 'standard',
    style: defaultStyle,
    audience: defaultAudience,
    overwrite: 'fill_empty'
  }));

  const patch = (next: Partial<OneDraftForm>) => setForm((cur) => ({ ...cur, ...next }));

  return { form, patch };
}
