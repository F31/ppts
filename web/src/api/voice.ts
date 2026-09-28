// 项目语音属性（模型 / 音色 / 语速）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ClientIdentity } from './identity';
import { getJSON, putJSON } from './http';
// ---- 项目语音属性（语音模型 / 音色 / 语速）----

export type ProjectVoiceSettings = {
  model: string;
  voice: string;
  ratePercent: number;
};

export type VoiceModel = {
  name: string;
  model: string;
  voices: string[];
  isDefault: boolean;
};

export async function getVoiceSettings(identity: ClientIdentity, projectId: string): Promise<ProjectVoiceSettings> {
  return getJSON<ProjectVoiceSettings>(identity, `/projects/${encodeURIComponent(projectId)}/voice-settings`);
}

export async function saveVoiceSettings(
  identity: ClientIdentity,
  projectId: string,
  settings: ProjectVoiceSettings
): Promise<ProjectVoiceSettings> {
  return putJSON<ProjectVoiceSettings>(identity, `/projects/${encodeURIComponent(projectId)}/voice-settings`, {
    model: settings.model,
    voice: settings.voice,
    ratePercent: settings.ratePercent
  });
}

// listVoiceModels 返回本租户已启用的 TTS 模型及其配置的音色；无可用模型时返回空数组。
export async function listVoiceModels(identity: ClientIdentity, projectId: string): Promise<VoiceModel[]> {
  const data = await getJSON<{ models?: VoiceModel[] }>(identity, `/projects/${encodeURIComponent(projectId)}/voice-models`);
  return data.models ?? [];
}
