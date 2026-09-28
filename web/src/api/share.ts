// 私密分享与协作者（#95）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { httpConnectError, requestJSON } from '../core';
import type { Collaborator, PlaybackManifest, ShareLink, SharedMeta } from '../types';
import type { ClientIdentity } from './identity';
import { deleteJSON, getJSON, postJSON, putJSON } from './http';
// ---- 私密分享与协作者（#95） ----
// 与"发布到公开作品广场"是两种不同能力：私密分享不出现在广场，只面向持有链接的人。
// 管理端走原生 HTTP 受保护端点；匿名端走 /shared/{token} 最小字段端点。

export async function listCollaborators(identity: ClientIdentity, projectId: string): Promise<Collaborator[]> {
  const data = await getJSON<{ collaborators?: Collaborator[] }>(identity, `/projects/${encodeURIComponent(projectId)}/collaborators`);
  return data.collaborators ?? [];
}

export async function inviteCollaborator(
  identity: ClientIdentity,
  projectId: string,
  params: { email: string; role: string }
): Promise<Collaborator> {
  const data = await postJSON<{ collaborator?: Collaborator }>(identity, `/projects/${encodeURIComponent(projectId)}/collaborators`, params);
  return data.collaborator!;
}

export async function updateCollaboratorRole(
  identity: ClientIdentity,
  projectId: string,
  userId: string,
  role: string
): Promise<Collaborator> {
  const data = await putJSON<{ collaborator?: Collaborator }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/collaborators/${encodeURIComponent(userId)}`,
    { role }
  );
  return data.collaborator!;
}

export async function removeCollaborator(identity: ClientIdentity, projectId: string, userId: string): Promise<void> {
  await deleteJSON<{ ok?: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/collaborators/${encodeURIComponent(userId)}`);
}

export async function listShareLinks(identity: ClientIdentity, projectId: string): Promise<ShareLink[]> {
  const data = await getJSON<{ shareLinks?: ShareLink[] }>(identity, `/projects/${encodeURIComponent(projectId)}/shares`);
  return data.shareLinks ?? [];
}

export async function createShareLink(
  identity: ClientIdentity,
  projectId: string,
  params: { accessMode: string; password?: string; expiresInDays?: number }
): Promise<ShareLink> {
  const data = await postJSON<{ shareLink?: ShareLink }>(identity, `/projects/${encodeURIComponent(projectId)}/shares`, params);
  return data.shareLink!;
}

export async function revokeShareLink(identity: ClientIdentity, projectId: string, linkId: string): Promise<void> {
  await postJSON<{ ok?: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/shares/${encodeURIComponent(linkId)}/revoke`, {});
}

// 匿名端：口令只经请求头传递（不进 URL / 访问日志）。
async function sharedGet<T>(path: string, password?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (password) headers['X-Share-Password'] = password;
  // 必须走 Connect 契约：SharedWatch 用 isNotFound 决定「链接无效/口令错误」的呈现，
  // 抛原生 Error 会让该判据恒为 false，把这两种正常业务语义显示成"加载失败"。
  // requestJSON 内部统一走 core.ts 的唯一 httpConnectError 实现。
  return requestJSON<T>({ method: 'GET', url: path, headers, label: `GET ${path}` });
}

export function getSharedMeta(token: string): Promise<SharedMeta> {
  return sharedGet<SharedMeta>(`/shared/${encodeURIComponent(token)}`);
}

export function getSharedManifest(token: string, password?: string): Promise<PlaybackManifest> {
  return sharedGet<PlaybackManifest>(`/shared/${encodeURIComponent(token)}/manifest`, password);
}

// shareUrl 把后端给的相对路径拼成绝对地址，供"复制链接"使用。
export function shareUrl(url: string): string {
  if (!url) return '';
  if (/^https?:\/\//i.test(url)) return url;
  return `${window.location.origin}${url.startsWith('/') ? url : `/${url}`}`;
}
