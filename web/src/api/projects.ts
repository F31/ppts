// 项目 / 幻灯片 / 源版本 / 页备注 / 渲染图
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { requestRaw } from '../core';
import type { Project, SlideSummary } from '../types';
import type { ClientIdentity } from './identity';
import { DOWNLOAD_TIMEOUT_MS, connectJSON, deleteJSON, getJSON, patchJSON, postJSON } from './http';
import { identityHeaders } from './identity';
export async function listProjects(identity: ClientIdentity): Promise<Project[]> {
  const data = await connectJSON<{ projects?: Project[] }>(identity, '/ppts.v1.ProjectService/List', { pageSize: 20 });
  return data.projects ?? [];
}

// listArchivedProjects 读取已归档项目（原生 HTTP），供“已归档”抽屉与恢复入口。
export type ArchivedProject = Project & {
  archivedAtUnix?: number;
  archivedBy?: string;
  archivedByName?: string;
  archivedByEmail?: string;
};

export async function listArchivedProjects(identity: ClientIdentity): Promise<ArchivedProject[]> {
  const data = await getJSON<{ projects?: ArchivedProject[] }>(identity, '/projects/archived');
  return data.projects ?? [];
}

// listAllProjects 分页拉取全部未归档项目（上限 cap），供任务列表映射 projectId → 标题/版本。
export async function listAllProjects(identity: ClientIdentity, cap = 500): Promise<Project[]> {
  const out: Project[] = [];
  let cursor = '';
  do {
    const page = await listProjectsPage(identity, { cursor, pageSize: 100 });
    out.push(...page.projects);
    cursor = page.nextCursor;
  } while (cursor && out.length < cap);
  return out;
}

// restoreProject 恢复已归档项目（需 ADMIN，与归档同级）。
export async function restoreProject(identity: ClientIdentity, projectId: string): Promise<void> {
  await postJSON(identity, `/projects/${encodeURIComponent(projectId)}/restore`, {});
}

export type ProjectPage = { projects: Project[]; nextCursor: string };

// listProjectsPage 带游标读取项目列表。
// ListProjectsResponse 只返回 projects + next_cursor，**没有总数**（proto/ppts/v1/project.proto:68），
// 因此 nextCursor 为空时才可把本页长度当作真实总数（D0-3，A04：分页 List 的本页长度不得当总数）。
export async function listProjectsPage(
  identity: ClientIdentity,
  params: { cursor?: string; pageSize: number }
): Promise<ProjectPage> {
  const data = await connectJSON<{ projects?: Project[]; nextCursor?: { value?: string } }>(
    identity,
    '/ppts.v1.ProjectService/List',
    {
      pageSize: params.pageSize,
      ...(params.cursor ? { cursor: { value: params.cursor } } : {})
    }
  );
  return { projects: data.projects ?? [], nextCursor: data.nextCursor?.value ?? '' };
}

// getProject 按 id 获取单个项目（含 title、currentRevision 等），供详情页展示。
export async function getProject(identity: ClientIdentity, id: string): Promise<Project> {
  return connectJSON<Project>(identity, '/ppts.v1.ProjectService/Get', { id });
}

export async function createProject(identity: ClientIdentity, title: string): Promise<Project> {
  const data = await connectJSON<{ project: Project }>(identity, '/ppts.v1.ProjectService/Create', { title });
  return data.project;
}

export async function archiveProject(identity: ClientIdentity, id: string): Promise<Project> {
  return connectJSON<Project>(identity, '/ppts.v1.ProjectService/Archive', { id });
}

export async function getProjectSlides(
  identity: ClientIdentity,
  projectId: string,
  revisionNo?: number
): Promise<{ revisionNo: number; slides: SlideSummary[] }> {
  const data = await connectJSON<{ revisionNo: number | string; slides?: SlideSummary[] }>(
    identity,
    '/ppts.v1.ProjectService/GetSlides',
    revisionNo && revisionNo > 0 ? { projectId, revisionNo } : { projectId }
  );
  // protojson 把 int64 序列化为字符串；这里统一归一为 number，避免与 REST 端点的 number
  // 做 `!==` 比较时恒为真（曾导致"配音版本与页面版本不匹配"而隐藏播放器）。
  return { slides: data.slides ?? [], revisionNo: Number(data.revisionNo) || 0 };
}

// SourceRevisionSummary 是版本历史列表项（原生 HTTP GET /projects/{pid}/revisions 返回）。
export type SourceRevisionSummary = {
  revisionNo: number;
  createdAt: string; // RFC3339
  pageCount: number;
  parserVersion: string;
  objectKey: string;
  displayName: string; // 默认取 objectKey 的文件名，可被前端覆盖
  isCurrent: boolean;
};

// parseObjectKeyDisplayName 从对象存储 key 提取文件名作为默认展示名。
function parseObjectKeyDisplayName(objectKey: string): string {
  const lastSlash = objectKey.lastIndexOf('/');
  return lastSlash >= 0 ? objectKey.slice(lastSlash + 1) : objectKey;
}

// getSourceRevisions 返回项目源版本历史（倒序）与当前生效版本号。
// 用于编辑器版本抽屉查看历史版本；切换"设为当前"不在此接口范围（后端暂无写接口）。
export async function getSourceRevisions(
  identity: ClientIdentity,
  projectId: string
): Promise<{ currentRevision: number; revisions: SourceRevisionSummary[] }> {
  const r = await getJSON<{
    current_revision: number;
    revisions: Array<{
      revision_no: number;
      created_at: string;
      page_count: number;
      parser_version: string;
      object_key: string;
      display_name?: string;
      is_current: boolean;
    }>;
  }>(identity, `/projects/${encodeURIComponent(projectId)}/revisions`);
  return {
    currentRevision: r.current_revision,
    revisions: (r.revisions ?? []).map((x) => ({
      revisionNo: x.revision_no,
      createdAt: x.created_at,
      pageCount: x.page_count,
      parserVersion: x.parser_version,
      objectKey: x.object_key,
      displayName: x.display_name || parseObjectKeyDisplayName(x.object_key),
      isCurrent: x.is_current,
    })),
  };
}

export async function deleteSourceRevision(identity: ClientIdentity, projectId: string, revisionNo: number): Promise<void> {
  await deleteJSON<{ ok: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}`);
}

export async function updatePptDisplayName(identity: ClientIdentity, projectId: string, revisionNo: number, displayName: string): Promise<void> {
  await patchJSON<Record<string, never>>(identity, `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}`, {
    display_name: displayName
  });
}

export async function downloadSourceRevision(
  identity: ClientIdentity,
  projectId: string,
  revisionNo: number,
  filename: string
): Promise<void> {
  const response = await requestRaw({
    method: 'GET',
    url: `/projects/${encodeURIComponent(projectId)}/revisions/${revisionNo}/download`,
    headers: identityHeaders(identity),
    // 下载体量取决于文件大小与网速，无法用统一的 15s 判定；给足但不给无限：
    // 超时必须最终产生失败态，而不是让用户面对一个永远在转却无法取消的界面（A26）。
    timeoutMs: DOWNLOAD_TIMEOUT_MS,
    label: `download revision ${revisionNo}`
  });
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename || `presentation-v${revisionNo}.pptx`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

// getSlideNotes 读取单页备注（空字符串 = 无备注）。
export async function getSlideNotes(identity: ClientIdentity, projectId: string, slideId: string, revisionNo: number): Promise<string> {
  const r = await getJSON<{ notes: string }>(identity, `/projects/${encodeURIComponent(projectId)}/slides/${encodeURIComponent(slideId)}/notes?revision_no=${revisionNo}`);
  return r.notes ?? '';
}

// setSlideNotes 保存单页备注。
export async function setSlideNotes(identity: ClientIdentity, projectId: string, slideId: string, revisionNo: number, notes: string): Promise<void> {
  await patchJSON<Record<string, never>>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/slides/${encodeURIComponent(slideId)}/notes?revision_no=${revisionNo}`,
    { notes }
  );
}

export type SlideRenderURL = { slideId: string; url: string };

// getSlideRenderURLs 返回每页渲染 PNG 的短期签名可读 URL（按 slideId 对齐），供编辑器缩略图与 PPT 预览使用。
// 解析未完成或页面图缺失时返回空列表，前端优雅降级为序号/标题缩略图。
// renderer：正常为空串；"unavailable" 表示服务端渲染器（LibreOffice/poppler）缺失，
// 本次解析没有产出任何页面图。用于把"环境没配置"与"加载失败/尚未渲染"区分开。
export async function getSlideRenderURLs(
  identity: ClientIdentity,
  projectId: string,
  revisionNo?: number
): Promise<{ slides: SlideRenderURL[]; renderer?: string }> {
  const qs = revisionNo && revisionNo > 0 ? `?revision_no=${revisionNo}` : '';
  return getJSON<{ slides: SlideRenderURL[]; renderer?: string }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/slides/render${qs}`
  );
}
