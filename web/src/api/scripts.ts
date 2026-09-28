// 讲稿：来源 / 草稿 / 修订 / 改写 / 重生成
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ScriptMode, ScriptRevision, ScriptSegment } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON, getJSON, postJSON, putJSON } from './http';
export type SlideScriptSource = { slideId: string; source: string; customText: string };

// setSlideScriptSource 持久化单页讲稿来源选择（M3 ⑥，无备注页显式指定驱动草稿来源）。
export async function setSlideScriptSource(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  source: string,
  customText = ''
): Promise<SlideScriptSource> {
  return putJSON<SlideScriptSource>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/slides/${encodeURIComponent(slideId)}/source`,
    { source, customText }
  );
}

// getSlideScriptSources 读取项目内所有页的讲稿来源选择（M3 ⑥）。
export async function getSlideScriptSources(
  identity: ClientIdentity,
  projectId: string
): Promise<{ sources: SlideScriptSource[] }> {
  return getJSON<{ sources: SlideScriptSource[] }>(identity, `/projects/${encodeURIComponent(projectId)}/slides/sources`);
}

export async function getScript(
  identity: ClientIdentity,
  projectId: string,
  slideId: string
): Promise<ScriptRevision> {
  return connectJSON<ScriptRevision>(identity, '/ppts.v1.ScriptService/Get', { projectId, slideId });
}

export async function listProjectScripts(
  identity: ClientIdentity,
  projectId: string
): Promise<{ scripts: ScriptRevision[] }> {
  return getJSON<{ scripts: ScriptRevision[] }>(identity, `/projects/${encodeURIComponent(projectId)}/scripts`);
}

export type UpdateScriptResult = {
  revision: ScriptRevision;
  conflict: boolean;
  latest?: ScriptRevision;
};

export async function updateScript(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  expectedRevision: number,
  segments: ScriptSegment[]
): Promise<UpdateScriptResult> {
  return connectJSON<UpdateScriptResult>(identity, '/ppts.v1.ScriptService/Update', {
    projectId,
    slideId,
    expectedRevision,
    segments
  });
}

// 讲稿「确认 / 锁定」的客户端封装（approveScript / lockScript / unlockScript）已移除：
// 产品决定改为「按需编辑、不锁定」（commit 40387c6 的免确认编辑），后端
// ScriptService/Approve、ScriptService/Lock 能力保留，但前端不再有入口，故不留死导出。

// regenerateSegments 局部重生成选中分段（M2 M1 落地的 RegenerateSegments RPC）。
// 返回 jobId；生成完成后需重新拉取讲稿。后端 NarrationService.RegenerateSegments。
export async function regenerateSegments(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  segmentIds: string[],
  voiceId?: string
): Promise<{ jobId: string }> {
  const idempotencyKey = `regen-${projectId}-${slideId}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return connectJSON<{ jobId: string }>(identity, '/ppts.v1.NarrationService/RegenerateSegments', {
    projectId,
    slideId,
    segmentIds,
    voiceId: voiceId ?? ''
  }, { 'Idempotency-Key': idempotencyKey });
}

// RewriteAction：段落工具栏"缩短/润色/衔接"对应的 LLM 改写动作，以及全页 AI 生成。
export type RewriteAction = 'shorten' | 'polish' | 'transition' | 'ai_generated';

export async function rewriteScriptText(
  identity: ClientIdentity,
  projectId: string,
  slideId: string,
  text: string,
  action: RewriteAction
): Promise<{ text: string }> {
  return postJSON<{ text: string }>(identity, `/projects/${encodeURIComponent(projectId)}/scripts/rewrite`, {
    slideId,
    text,
    action
  });
}

// 说明（M6 清理）：此处原有 generateDraft（Connect ScriptService/GenerateDraft）已删除——
// 全站无调用方，且它未传 RevisionNo/SourceMode，一旦被启用会让 worker 回退到首个版本
// （旧版本可能页数不足/没有备注 → "有备注的页没有讲稿"）。重生成讲稿统一走
// regenerateScriptDraft（原生端点，显式绑定当前版本并透传 sourceMode/overwrite）。

// regenerateScriptDraft 重新生成讲稿。默认 overwrite=true；一键成稿可传 overwrite=false 只填充空白页。
export async function regenerateScriptDraft(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  mode: ScriptMode,
  options: {
    language?: string;
    sourceMode?: 'notes_first' | 'page_content' | 'notes_only';
    audience?: string;
    style?: string;
    targetSeconds?: number;
    overwrite?: boolean;
  } = {}
): Promise<{ jobId: string }> {
  return postJSON<{ jobId: string }>(identity, `/projects/${encodeURIComponent(projectId)}/script-draft`, {
    slideIds,
    mode,
    language: options.language,
    sourceMode: options.sourceMode,
    audience: options.audience,
    style: options.style,
    targetSeconds: options.targetSeconds,
    overwrite: options.overwrite
  });
}
