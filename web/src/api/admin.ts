// 运营商后台：跨租户查看与挂起/恢复
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ClientIdentity } from './identity';
import { getJSON, postJSON } from './http';
// ---- 运营商后台（第二批）：跨租户查看与挂起/恢复 ----
export type AdminTenantQuota = {
  kind: string;
  limit_units: number;
  reserved_units: number;
  consumed_units: number;
  available_units: number;
  unlimited: boolean;
};

export type AdminTenant = {
  id: string;
  name: string;
  type: string;
  status: string;
  created_at: string;
  quota?: AdminTenantQuota;
};

export type AdminTenantsPage = {
  tenants: AdminTenant[];
  nextCursor: string;
  total: number;
};

export async function listAdminTenants(
  identity: ClientIdentity,
  opts?: { cursor?: string; pageSize?: number }
): Promise<AdminTenantsPage> {
  const q = new URLSearchParams();
  if (opts?.cursor) q.set('cursor', opts.cursor);
  if (opts?.pageSize) q.set('page_size', String(opts.pageSize));
  const suffix = q.toString() ? `?${q.toString()}` : '';
  const r = await getJSON<{ tenants: AdminTenant[]; next_cursor: string; total: number }>(
    identity,
    `/admin/tenants${suffix}`
  );
  return { tenants: r.tenants ?? [], nextCursor: r.next_cursor ?? '', total: r.total ?? 0 };
}

export async function suspendTenant(identity: ClientIdentity, tenantId: string): Promise<void> {
  await postJSON(identity, `/admin/tenants/${encodeURIComponent(tenantId)}/suspend`, {});
}

export async function resumeTenant(identity: ClientIdentity, tenantId: string): Promise<void> {
  await postJSON(identity, `/admin/tenants/${encodeURIComponent(tenantId)}/resume`, {});
}
