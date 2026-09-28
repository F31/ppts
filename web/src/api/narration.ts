// 配音：一键成稿任务 / 估算 / 成品 / 过期检测
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { PlaybackManifest } from '../types';
import type { ClientIdentity } from './identity';
import { getProjectSlides } from './projects';
import { connectJSON, deleteJSON, getJSON } from './http';
export async function createGeneration(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  voiceId: string,
  idempotencyKey: string,
  opts: { ratePercent?: number; lockConfirmedOnly?: boolean; bypassCache?: boolean } = {}
): Promise<{ jobId: string; withinBudget: boolean }> {
  // D0-1：前端此前漏传 ratePercent / lockConfirmedOnly，导致后端 C-5 强制阻止未确认稿与
  // 配额预占比例从未生效。这里补全：ratePercent 默认 100（全速），lockConfirmedOnly 默认 false。
  // bypassCache：用户显式要求"重新生成"时置真，要求后端跳过内容哈希缓存、真实调用语音合成
  // （否则音色/讲稿未变时全部命中缓存，界面承诺的"重新生成并覆盖"实际什么都没合成）。
  const headers: Record<string, string> = { 'Idempotency-Key': idempotencyKey };
  if (opts.bypassCache) headers['X-PPTS-Bypass-Cache'] = 'true';
  return connectJSON<{ jobId: string; withinBudget: boolean }>(
    identity,
    '/ppts.v1.NarrationService/CreateGeneration',
    {
      projectId,
      slideIds,
      voiceId,
      ratePercent: opts.ratePercent ?? 100,
      lockConfirmedOnly: opts.lockConfirmedOnly ?? false
    },
    headers
  );
}

export type NarrationEstimate = {
  currency: string;
  costMin: number;
  costMax: number;
  estimatedSeconds: number;
};

export async function estimateNarration(
  identity: ClientIdentity,
  projectId: string,
  slideIds: string[],
  voiceId: string,
  ratePercent = 100
): Promise<NarrationEstimate> {
  const data = await connectJSON<{ currency?: string; costMin?: number; costMax?: number; estimatedSeconds?: number }>(
    identity,
    '/ppts.v1.NarrationService/Estimate',
    { projectId, slideIds, voiceId, ratePercent }
  );
  return {
    currency: data.currency ?? '',
    costMin: data.costMin ?? 0,
    costMax: data.costMax ?? 0,
    estimatedSeconds: data.estimatedSeconds ?? 0
  };
}

// getNarrationDraftCount 读取项目内仍处于 draft 状态的讲稿分段总数（M1 端点，C-5 生成前置检查用）。
// 返回 { draftSegments }，>0 表示存在未确认讲稿，正式生成应被阻止。
export async function getNarrationDraftCount(
  identity: ClientIdentity,
  projectId: string
): Promise<{ draftSegments: number }> {
  return getJSON<{ draftSegments: number }>(identity, `/projects/${encodeURIComponent(projectId)}/narration/draft-count`);
}

export type ProjectArtifact = {
  id: string;
  snapshotHash: string;
  format: 'mp4' | 'srt' | 'vtt' | 'web_project';
  sizeBytes: number;
  // durationMs：成品所绑定时间轴的实际时长；0 = 未知/未记录（迁移 0027 之前的历史行），显示「—」。
  durationMs: number;
  createdAt: string;
  downloadable: boolean;
};

// getProjectArtifacts 读取项目下全部产物（B3-M1 原生端点，前端按 snapshotHash 分组展示与下载）。
export async function getProjectArtifacts(
  identity: ClientIdentity,
  projectId: string
): Promise<{ artifacts: ProjectArtifact[] }> {
  return getJSON<{ artifacts: ProjectArtifact[] }>(identity, `/projects/${encodeURIComponent(projectId)}/artifacts`);
}

// getLibraryArtifacts 读取租户内全部项目的成品（B5-M2 跨项目成品库，owner 级）。
// 返回项含 projectId / projectName，前端按格式 / 项目 / 时间筛选，并可跳转项目成品页。
export type LibraryArtifact = {
  id: string;
  projectId: string;
  projectName: string;
  snapshotHash: string;
  format: 'mp4' | 'srt' | 'vtt' | 'web_project';
  sizeBytes: number;
  // durationMs：0 = 未知/未记录（迁移 0027 之前的历史行），显示「—」。
  durationMs: number;
  createdAt: string;
  downloadable: boolean;
  // previewable：成品绑定了导出时的时间轴（迁移 0040 之后导出），可内嵌预览；
  // false（历史行）时前端隐藏"预览"按钮，降级为仅下载。
  previewable: boolean;
  // revisionNo：成品所源自的源版本号（0 = 未知/历史行），与 sourceDisplayName 一并展示"PPT 名称 vN"；
  // 由 narration 写入时间轴、export 落库（见 internal/app/{narration,export}.go）。
  revisionNo?: number;
  // sourceDisplayName：源版本展示名（source_revisions.display_name，空 = 未知），
  // 空时前端回退到 projectName。
  sourceDisplayName?: string;
};

export async function getLibraryArtifacts(identity: ClientIdentity): Promise<{ artifacts: LibraryArtifact[] }> {
  return getJSON<{ artifacts: LibraryArtifact[] }>(identity, '/artifacts');
}

// getArtifactManifest 取成品内嵌预览用的播放清单（GET /artifacts/{id}/manifest）：
// 以成品绑定的时间轴为数据源，与下载文件同源；返回结构与 Player 的 PlaybackManifest 一致。
export async function getArtifactManifest(identity: ClientIdentity, artifactId: string): Promise<PlaybackManifest> {
  return getJSON<PlaybackManifest>(identity, `/artifacts/${encodeURIComponent(artifactId)}/manifest`);
}

// deleteArtifact 删除成品库中的成品（DELETE /artifacts/{id}，owner 级，与 GET /artifacts 同门禁）。
export async function deleteArtifact(identity: ClientIdentity, artifactId: string): Promise<{ deleted: boolean; id: string }> {
  return deleteJSON<{ deleted: boolean; id: string }>(identity, `/artifacts/${encodeURIComponent(artifactId)}`);
}

export type NarrationSlideStale = { slideId: string; stale: boolean };

export type RevisionVoiceStatus = {
  revisionNo: number;
  status: 'not_voiced' | 'partial' | 'complete' | 'stale';
  pageCount: number;
  voicedPages: number;
  missingPages: number;
  stalePages: number;
  missingSlideIds?: string[];
  staleSlideIds?: string[];
};

export async function getRevisionVoiceStatus(
  identity: ClientIdentity,
  projectId: string
): Promise<{ revisions: RevisionVoiceStatus[] }> {
  return getJSON<{ revisions: RevisionVoiceStatus[] }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/revisions/voice-status`
  );
}

// getNarrationStale 读取项目内"讲稿已改、配音未重生成"的页（首页「音频需更新」的真实信号）。
// 后端判定：audio_revision < revision（internal/api/narration.go:373），要求 editor 角色。
export async function getNarrationStale(
  identity: ClientIdentity,
  projectId: string
): Promise<{ slides: NarrationSlideStale[] }> {
  return getJSON<{ slides: NarrationSlideStale[] }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/narration/stale`
  );
}

// SynthesisStats 是本次配音任务的音频来源构成。
//
// 存在理由：音色/讲稿/语速均未变化时，分段会命中内容哈希缓存——时间轴重建了，但音频沿用
// 既有对象、并未重新合成。少了它，「全部（重新生成并覆盖）」会把"复用旧音频"报成"已重新生成"。
// 旧任务或未产出时为 undefined/null，此时不得推断，只能不提。
export type SynthesisStats = {
  segments: number;
  synthesized: number;
  cached: number;
};

export type NarrationStatus = {
  ready: boolean;
  timelineKey: string;
  pagePngKeys: string[];
  revisionNo: number;
  synthesis?: SynthesisStats | null;
};

export async function getNarration(identity: ClientIdentity, projectId: string): Promise<NarrationStatus> {
  const data = await connectJSON<NarrationStatus & { revisionNo?: number | string }>(
    identity,
    '/ppts.v1.PlaybackService/GetNarration',
    { projectId }
  );
  // protojson 的 int64 → 字符串，归一为 number（见 getProjectSlides 注释）。
  return { ...data, revisionNo: Number(data.revisionNo) || 0 };
}

// getRevisionNarration 读取指定源版本的配音状态（ready/timelineKey/pagePngKeys/revisionNo）。
// 后端按 snapshot.RevisionNo 归集该版本自己的成功配音任务（见 editorRevisionNarration），
// 供 PPT 列表页「导出」按钮就地弹 ExportDialog 时取素材。
export async function getRevisionNarration(
  identity: ClientIdentity,
  projectId: string,
  revisionNo: number
): Promise<NarrationStatus> {
  const data = await getJSON<NarrationStatus & { revisionNo?: number | string }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}/narration`
  );
  return { ...data, revisionNo: Number(data.revisionNo) || 0 };
}
