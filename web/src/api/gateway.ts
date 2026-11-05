// 模型网关（LLM/TTS 上游配置，admin only）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { requestJSON } from '../core';
import type { ClientIdentity } from './identity';
import { identityHeaders } from './identity';
// ---- 模型网关管理（G3 可视化配置，admin only）----

export type ModelGateway = {
  tenantId: string;
  name: string;
  kind: 'tts' | 'llm';
  provider: string;
  baseUrl: string;
  model: string;
  visionModel: string;
  voice: string;
  sampleRate: number;
  isDefault: boolean;
  enabled: boolean;
  version: number;
  hasKey: boolean;
  keyMasked: string;
  // 后端确实存了密文但当前 AES 密钥解不开；与「未配置 key」是两回事，不能引导用户重填。
  keyUnreadable?: boolean;
  createdAt: number;
  updatedAt: number;
  // source 由管理面列表下发：tenant = 本租户自己的配置；platform = 平台共享（只读）。
  // 旧后端/单条响应可能不带该字段，按 tenant 处理（可编辑）。
  source?: 'tenant' | 'platform';
};

export type GatewayTestResult = { ok: boolean; latencyMs: number; error?: string };

export function gatewayPath(identity: ClientIdentity, method: string, path: string, body?: unknown, timeoutMs?: number): Promise<unknown> {
  return requestJSON<unknown>({
    method,
    url: path,
    headers: { 'Content-Type': 'application/json', ...identityHeaders(identity) },
    body,
    label: `${method} ${path}`,
    timeoutMs
  });
}

export type GatewayScope = 'tenant' | 'platform';

export async function listGateways(identity: ClientIdentity, kind?: 'tts' | 'llm', scope: GatewayScope = 'tenant'): Promise<ModelGateway[]> {
  const suffix = `?scope=${scope}${kind ? `&kind=${kind}` : ''}`;
  const data = (await gatewayPath(identity, 'GET', `/api/model-gateways${suffix}`)) as { gateways?: ModelGateway[] };
  return data.gateways ?? [];
}

export async function createGateway(
  identity: ClientIdentity,
  input: { kind: 'tts' | 'llm'; name: string; baseUrl: string; apiKey: string; model: string; provider?: string; visionModel?: string; voice?: string; sampleRate?: number; isDefault?: boolean },
  scope: GatewayScope = 'tenant'
): Promise<ModelGateway> {
  const data = (await gatewayPath(identity, 'POST', `/api/model-gateways?scope=${scope}`, input)) as { gateway?: ModelGateway };
  return data.gateway!;
}

export async function updateGateway(
  identity: ClientIdentity,
  name: string,
  input: { kind: 'tts' | 'llm'; originalKind?: 'tts' | 'llm'; version: number; baseUrl?: string; apiKey?: string; model?: string; provider?: string; visionModel?: string; voice?: string; sampleRate?: number; isDefault?: boolean; enabled?: boolean },
  scope: GatewayScope = 'tenant'
): Promise<ModelGateway> {
  const data = (await gatewayPath(identity, 'PUT', `/api/model-gateways/${encodeURIComponent(name)}?scope=${scope}`, input)) as { gateway?: ModelGateway };
  return data.gateway!;
}

export async function deleteGateway(identity: ClientIdentity, name: string, kind: 'tts' | 'llm', scope: GatewayScope = 'tenant'): Promise<void> {
  await gatewayPath(identity, 'DELETE', `/api/model-gateways/${encodeURIComponent(name)}?kind=${kind}&scope=${scope}`);
}

export async function setDefaultGateway(identity: ClientIdentity, name: string, kind: 'tts' | 'llm', scope: GatewayScope = 'tenant'): Promise<void> {
  await gatewayPath(identity, 'POST', `/api/model-gateways/${encodeURIComponent(name)}/set-default?kind=${kind}&scope=${scope}`);
}

// source=platform 让租户作用域能测试「列表里可见的平台共享行」：后端会校验该行确实
// 可见（平台有、且本租户没有同名行），再用平台租户解析凭据。只读 ≠ 不可验证；
// 不加该参数时保持原语义，只测本租户自己的行。
export async function testGateway(
  identity: ClientIdentity,
  name: string,
  kind: 'tts' | 'llm',
  scope: GatewayScope = 'tenant',
  source?: 'platform'
): Promise<GatewayTestResult> {
  const suffix = source ? `&source=${source}` : '';
  return (await gatewayPath(identity, 'POST', `/api/model-gateways/${encodeURIComponent(name)}/test?kind=${kind}&scope=${scope}${suffix}`, undefined, 65000)) as GatewayTestResult;
}
