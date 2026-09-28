// 上下文替换规则（M5 数据驱动）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ContextRule } from '../types';
import type { ClientIdentity } from './identity';
import { gatewayPath } from './gateway';
// ---- 上下文替换规则（M5 数据驱动，/api/context-rules，需认证，tenant 隔离） ----

export async function listContextRules(identity: ClientIdentity): Promise<ContextRule[]> {
  const data = (await gatewayPath(identity, 'GET', '/api/context-rules')) as { rules?: ContextRule[] };
  return data.rules ?? [];
}

export async function createContextRule(
  identity: ClientIdentity,
  input: { pattern: string; replacement: string; priority: number; enabled: boolean }
): Promise<ContextRule> {
  const data = (await gatewayPath(identity, 'POST', '/api/context-rules', input)) as { rule?: ContextRule };
  return data.rule!;
}

export async function updateContextRule(
  identity: ClientIdentity,
  id: string,
  input: { pattern: string; replacement: string; priority: number; enabled: boolean }
): Promise<ContextRule> {
  const data = (await gatewayPath(identity, 'PUT', `/api/context-rules/${encodeURIComponent(id)}`, input)) as { rule?: ContextRule };
  return data.rule!;
}

export async function deleteContextRule(identity: ClientIdentity, id: string): Promise<void> {
  await gatewayPath(identity, 'DELETE', `/api/context-rules/${encodeURIComponent(id)}`);
}
