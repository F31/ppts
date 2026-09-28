// 审计事件与归档导出
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { AuditArchiveFile, AuditEvent } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON } from './http';
export async function listAuditEvents(
  identity: ClientIdentity,
  params: { action?: string; resourceType?: string; sinceUnix?: number; pageSize?: number }
): Promise<AuditEvent[]> {
  const data = await connectJSON<{ events?: AuditEvent[] }>(identity, '/ppts.v1.TenantService/ListAuditEvents', {
    action: params.action ?? '',
    resourceType: params.resourceType ?? '',
    sinceUnix: params.sinceUnix ?? 0,
    pageSize: params.pageSize ?? 50
  });
  return data.events ?? [];
}

export async function listAuditArchives(identity: ClientIdentity, limit = 20): Promise<AuditArchiveFile[]> {
  const data = await connectJSON<{ files?: AuditArchiveFile[] }>(identity, '/ppts.v1.TenantService/ListAuditArchives', { limit });
  return data.files ?? [];
}

export async function sha256Hex(file: File): Promise<string> {
  const buffer = await file.arrayBuffer();
  const digest = await crypto.subtle.digest('SHA-256', buffer);
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('');
}
