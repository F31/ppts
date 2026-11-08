// 成品级分享链接与作者侧观看统计（M6-3 前端接线 + V20 数据出口）。
//
// 与 api/share.ts 刻意分开：那边是**项目**私密分享（/projects/{pid}/shares），
// 这里是**成品**分享（/projects/{pid}/artifacts/{aid}/shares）。两者的匿名落点、
// 锚点语义与鉴权都不同，合成一个文件迟早会有人把 pid 当 aid 传。
//
// 路径写成完整模板字面量而不是拼 basePath：api-contract-check.mjs 只会把以 `/` 开头的
// 字面量拿去和 Go 路由表比对，用 `${base()}/...` 开头会静默绕过这条检查。

import { getJSON, postJSON } from './http';
import type { ClientIdentity } from './identity';
import type { ArtifactShareLink, ArtifactShareStatsResponse } from '../types';

export async function listArtifactShareLinks(
  identity: ClientIdentity, projectId: string, artifactId: string
): Promise<ArtifactShareLink[]> {
  const data = await getJSON<{ shareLinks?: ArtifactShareLink[] }>(
    identity, `/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares`);
  return data.shareLinks ?? [];
}

export async function createArtifactShareLink(
  identity: ClientIdentity, projectId: string, artifactId: string,
  params: { accessMode?: string; password?: string; expiresInDays?: number }
): Promise<ArtifactShareLink> {
  const data = await postJSON<{ shareLink?: ArtifactShareLink }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares`,
    params
  );
  return data.shareLink!;
}

export async function revokeArtifactShareLink(
  identity: ClientIdentity, projectId: string, artifactId: string, linkId: string
): Promise<void> {
  await postJSON<{ ok?: boolean }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares/${encodeURIComponent(linkId)}/revoke`,
    {}
  );
}

export async function getArtifactShareStats(
  identity: ClientIdentity, projectId: string, artifactId: string, linkId: string, days: number
): Promise<ArtifactShareStatsResponse> {
  return getJSON<ArtifactShareStatsResponse>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares/${encodeURIComponent(linkId)}/stats?days=${encodeURIComponent(String(days))}`
  );
}
