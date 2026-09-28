// 公开区：发布 / 精选 / 审核 / 匿名访问
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { requestJSON } from '../core';
import type { PlaybackManifest } from '../types';
import type { ClientIdentity } from './identity';
import { getPlaybackManifest } from './playback';
import { connectJSON } from './http';
import { identityHeaders } from './identity';
// ---- 公开区（/public/*，V1.6 C-1） ----
// 匿名只读接口（list/get）无需身份；写接口（发布/精选/审核/删除）带身份头。

export type PublicationKind = 'featured' | 'user';
export type PublicationStatus = 'draft' | 'pending' | 'approved' | 'rejected' | 'withdrawn';

// PublicWork 字段名与后端 JSON（snake_case）一致，避免额外映射层。
export type PublicWork = {
  id: string;
  public_id?: string;
  tenant_id: string;
  project_id: string;
  kind: PublicationKind;
  status: PublicationStatus;
  title: string;
  summary: string;
  cover_object_key?: string;
  cover_url?: string;
  sort_order: number;
  created_by: string;
  created_at: string; // RFC3339
  updated_at?: string;
  reviewed_by?: string;
  reviewed_at?: string;
};

export type PublicWorkPage = { items: PublicWork[]; next_cursor: string };

async function publicGet<T>(path: string): Promise<T> {
  return requestJSON<T>({ method: 'GET', url: path, label: `GET ${path}` });
}

export async function listPublicWorks(params: { kind?: PublicationKind; cursor?: string; limit?: number } = {}): Promise<PublicWorkPage> {
  const qs = new URLSearchParams();
  if (params.kind) qs.set('kind', params.kind);
  if (params.cursor) qs.set('cursor', params.cursor);
  if (params.limit) qs.set('limit', String(params.limit));
  const q = qs.toString();
  return publicGet<PublicWorkPage>(`/public/works${q ? `?${q}` : ''}`);
}

// getShowcaseWork 拉取已批准公开作品详情（B5-M3：按不可反推的 public_id 匿名访问 /showcase/{publicId}）。
export async function getShowcaseWork(publicId: string): Promise<PublicWork> {
  return publicGet<PublicWork>(`/showcase/${encodeURIComponent(publicId)}`);
}

// getShowcaseManifest 拉取已批准公开作品的匿名可播放讲解清单（B3 音频播放接入）。
// 与控制台 getPlaybackManifest 同构，但走原生 HTTP 匿名端点、无需鉴权；narration 未就绪时返回 404。
export async function getShowcaseManifest(publicId: string): Promise<PlaybackManifest> {
  return publicGet<PlaybackManifest>(`/showcase/${encodeURIComponent(publicId)}/manifest`);
}

export async function publishWork(
  identity: ClientIdentity,
  input: { projectId: string; title: string; summary?: string; coverObjectKey?: string }
): Promise<PublicWork> {
  return connectJSON<PublicWork>(identity, '/public/works', {
    project_id: input.projectId,
    title: input.title,
    summary: input.summary ?? '',
    cover_object_key: input.coverObjectKey ?? ''
  });
}

export async function featureWork(
  identity: ClientIdentity,
  input: { projectId: string; title: string; summary?: string; coverObjectKey?: string }
): Promise<PublicWork> {
  return connectJSON<PublicWork>(identity, '/public/featured', {
    project_id: input.projectId,
    title: input.title,
    summary: input.summary ?? '',
    cover_object_key: input.coverObjectKey ?? ''
  });
}

// authedJSON 兼容非 POST 方法（审核用 PUT、删除用 DELETE）。
async function authedJSON<T>(identity: ClientIdentity, method: string, path: string, body?: unknown): Promise<T> {
  // 204/空体统一返回 undefined（同源行为见 core.ts requestJSON 注释）。
  return requestJSON<T>({
    method,
    url: path,
    headers: { 'Content-Type': 'application/json', ...identityHeaders(identity) },
    body,
    label: `${method} ${path}`
  });
}

export async function reviewWork(identity: ClientIdentity, id: string, approve: boolean): Promise<PublicWork> {
  return authedJSON<PublicWork>(identity, 'PUT', `/public/works/${encodeURIComponent(id)}/review`, { approve });
}

export async function deleteWork(identity: ClientIdentity, id: string): Promise<void> {
  await authedJSON<Record<string, never>>(identity, 'DELETE', `/public/works/${encodeURIComponent(id)}`);
}

// recallWork 由 owner/admin 撤回已发布作品（置 withdrawn 立即失效，B5-M3）。
export async function recallWork(identity: ClientIdentity, publicId: string): Promise<PublicWork> {
  return authedJSON<PublicWork>(identity, 'POST', `/public/works/${encodeURIComponent(publicId)}/recall`);
}

// 受保护只读：我的发布（按创建者）/ 审核队列（admin，pending）。
export async function listMyPublications(identity: ClientIdentity, params: { status?: PublicationStatus } = {}): Promise<PublicWorkPage> {
  const qs = new URLSearchParams();
  if (params.status) qs.set('status', params.status);
  const q = qs.toString();
  return (await authedJSON<PublicWorkPage>(identity, 'GET', `/public/works/mine${q ? `?${q}` : ''}`)) as PublicWorkPage;
}

export async function listReviewQueue(identity: ClientIdentity, params: { kind?: PublicationKind } = {}): Promise<PublicWorkPage> {
  const qs = new URLSearchParams();
  if (params.kind) qs.set('kind', params.kind);
  const q = qs.toString();
  return (await authedJSON<PublicWorkPage>(identity, 'GET', `/public/works/queue${q ? `?${q}` : ''}`)) as PublicWorkPage;
}
