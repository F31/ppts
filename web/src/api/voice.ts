// 项目语音属性（模型 / 音色 / 语速）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ClientIdentity } from './identity';
import { identityHeaders, languageHeader } from './identity';
import { getJSON, putJSON } from './http';
import { ConnectError, requestRaw } from '../core';
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

export type VoicePreviewResult = {
  blob: Blob;
  // durationMs 是后端解码出的**真实**时长（不是按字数估的），用于界面上标注试听长度。
  durationMs: number;
  // model 是这次试听实际使用的模型；为空表示未经网关解析（测试/离线注入）。
  model: string;
};

// previewVoice 合成一段短样音（≤120 字）。返回 Blob 而不是 URL：
// URL 的生命周期（revokeObjectURL）必须由组件管理，API 层擅自创建会泄漏。
export async function previewVoice(
  identity: ClientIdentity,
  projectId: string,
  body: { voice: string; ratePercent: number; text?: string; language?: string }
): Promise<VoicePreviewResult> {
  const response = await requestRaw({
    method: 'POST',
    url: `/projects/${encodeURIComponent(projectId)}/voice-preview`,
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body: { voice: body.voice, ratePercent: body.ratePercent, text: body.text, language: body.language },
    label: 'POST voice-preview',
    // 合成要等上游，且不能与普通读接口共用 10s 预算：后端自己 20s 超时，这里留出往返余量。
    timeoutMs: 30_000
  });
  const blob = await response.blob();
  // 200 + 0 字节在 <audio> 里就是"点了没反应"。后端已挡住这种情况，
  // 但反向代理/中间层也可能返回空体，前端必须自己判一次。
  if (blob.size === 0) {
    throw new ConnectError('tts_failed', 'voice preview returned empty audio');
  }
  const duration = Number(response.headers.get('X-PPTS-Audio-Duration-Ms') ?? '');
  return {
    blob,
    durationMs: Number.isFinite(duration) && duration > 0 ? duration : 0,
    model: response.headers.get('X-PPTS-Preview-Model') ?? ''
  };
}
