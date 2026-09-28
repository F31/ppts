// 租户：成员 / 标签 / 分组 / 项目组织 / 额度用量
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { Folder, Member, ProjectOrg, ProjectUsage, Role, StorageUsage, Tag, TenantPolicy, TenantQuota, TenantUsage } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON, deleteJSON, getJSON, postJSON, putJSON } from './http';
// ---- 租户（TenantService） ----

export async function listMembers(identity: ClientIdentity): Promise<Member[]> {
  const data = await getJSON<{ members?: {
    user_id: string;
    role: Role;
    created_at?: string;
    email?: string;
    username?: string;
    full_name?: string;
    gender?: string;
    birth_date?: string;
    phone?: string;
  }[] }>(identity, '/members');
  return (data.members ?? []).map((member) => ({
    userId: member.user_id,
    role: member.role,
    createdAt: member.created_at,
    email: member.email,
    username: member.username,
    fullName: member.full_name,
    gender: member.gender,
    birthDate: member.birth_date,
    phone: member.phone
  }));
}

// updateMemberProfile 写入成员档案（admin 级），供成员列表富字段展示。
export async function updateMemberProfile(
  identity: ClientIdentity,
  userId: string,
  profile: Partial<Pick<Member, 'username' | 'fullName' | 'gender' | 'birthDate' | 'phone'>>
): Promise<void> {
  await putJSON(identity, `/members/${encodeURIComponent(userId)}`, profile as Record<string, unknown>);
}

// ---- #94 标签 + 分组体系（原生 HTTP 端点） ----

export async function listTags(identity: ClientIdentity): Promise<Tag[]> {
  const data = await getJSON<{ tags?: Tag[] }>(identity, '/tags');
  return data.tags ?? [];
}

export async function createTag(identity: ClientIdentity, name: string, color?: string): Promise<Tag> {
  return postJSON<Tag>(identity, '/tags', { name, color: color ?? '' });
}

export async function renameTag(identity: ClientIdentity, tagId: string, name: string, color?: string): Promise<Tag> {
  return putJSON<Tag>(identity, `/tags/${encodeURIComponent(tagId)}`, { name, color: color ?? '' });
}

export async function deleteTag(identity: ClientIdentity, tagId: string): Promise<void> {
  await deleteJSON<{ ok: boolean }>(identity, `/tags/${encodeURIComponent(tagId)}`);
}

export async function listFolders(identity: ClientIdentity): Promise<Folder[]> {
  const data = await getJSON<{ folders?: {
    id: string;
    name: string;
    created_by?: string;
    sort_order?: number;
    created_at?: string;
  }[] }>(identity, '/folders');
  return (data.folders ?? []).map((folder) => ({
    id: folder.id,
    name: folder.name,
    createdBy: folder.created_by,
    sortOrder: folder.sort_order,
    createdAt: folder.created_at
  }));
}

export async function createFolder(identity: ClientIdentity, name: string, afterFolderId = ''): Promise<Folder> {
  const folder = await postJSON<{ id: string; name: string; created_by?: string; sort_order?: number; created_at?: string }>(identity, '/folders', {
    name,
    after_folder_id: afterFolderId
  });
  return {
    id: folder.id,
    name: folder.name,
    createdBy: folder.created_by,
    sortOrder: folder.sort_order,
    createdAt: folder.created_at
  };
}

export async function renameFolder(identity: ClientIdentity, folderId: string, name: string): Promise<Folder> {
  return putJSON<Folder>(identity, `/folders/${encodeURIComponent(folderId)}`, { name });
}

export async function deleteFolder(identity: ClientIdentity, folderId: string): Promise<void> {
  await deleteJSON<{ ok: boolean }>(identity, `/folders/${encodeURIComponent(folderId)}`);
}

export async function listProjectOrganization(identity: ClientIdentity): Promise<ProjectOrg[]> {
  const data = await getJSON<{ organization?: ProjectOrg[] }>(identity, '/projects/organization');
  return data.organization ?? [];
}

export async function attachTag(identity: ClientIdentity, projectId: string, tagId: string): Promise<void> {
  await putJSON<{ ok: boolean }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/tags/${encodeURIComponent(tagId)}`,
    {}
  );
}

export async function detachTag(identity: ClientIdentity, projectId: string, tagId: string): Promise<void> {
  await deleteJSON<{ ok: boolean }>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/tags/${encodeURIComponent(tagId)}`
  );
}

export async function moveProject(identity: ClientIdentity, projectId: string, folderId: string): Promise<void> {
  await putJSON<{ ok: boolean }>(identity, `/projects/${encodeURIComponent(projectId)}/folder`, { folderId });
}

export async function setMemberRole(identity: ClientIdentity, userId: string, role: Role): Promise<Member> {
  const data = await connectJSON<{ member?: Member }>(identity, '/ppts.v1.TenantService/SetMemberRole', { userId, role });
  return data.member!;
}

export async function removeMember(identity: ClientIdentity, userId: string): Promise<void> {
  await connectJSON<Record<string, never>>(identity, '/ppts.v1.TenantService/RemoveMember', { userId });
}

export async function getQuota(identity: ClientIdentity): Promise<TenantQuota> {
  return connectJSON<TenantQuota>(identity, '/ppts.v1.TenantService/Quota', {});
}

export async function getUsage(identity: ClientIdentity, month?: string): Promise<TenantUsage> {
  return connectJSON<TenantUsage>(identity, '/ppts.v1.TenantService/Usage', { month: month ?? '' });
}

export async function getProjectUsage(identity: ClientIdentity, projectId: string): Promise<ProjectUsage> {
  return connectJSON<ProjectUsage>(identity, '/ppts.v1.TenantService/ProjectUsage', { projectId });
}

export async function getStorageUsage(identity: ClientIdentity): Promise<StorageUsage> {
  return connectJSON<StorageUsage>(identity, '/ppts.v1.TenantService/StorageUsage', {});
}

export async function getPolicy(identity: ClientIdentity): Promise<TenantPolicy> {
  return connectJSON<TenantPolicy>(identity, '/ppts.v1.TenantService/Policy', {});
}
