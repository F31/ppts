// 模型服务「三态」判定（B4-M2 / A23）。
//
// 事实约束（逐文件核实，勿凭记忆修改）：
// - `model_gateways` 表只有配置字段，**没有** tested_at / status 列（migrations/0023_model_gateways.sql），
//   因此「检测通过 / 当前不可用」只能来自**本次会话内**的 `POST /api/model-gateways/{name}/test` 结果，
//   刷新页面即回到「未检测」。未检测 ≠ 不可用，UI 必须显式标注。
// - worker 侧真实 TTS 供应商由环境变量 `PPTS_TTS_PROVIDER`（fake | siliconflow）决定
//   （cmd/worker/main.go:56、:351），**不经任何 HTTP/RPC 端点暴露**。控制台无法直接得知，
//   故不渲染 `fake` 这类内部枚举（见 docs/PPT讲解平台-控制台UI设计评审.md:53、:57），
//   只用可验证的等价信号：「本租户是否接入了可用（启用 + 有密钥）的网关」。
import type { GatewayTestResult, ModelGateway } from './api';

/** 单个网关的健康态（5 态：3 态主判定 + 无密钥/已禁用细分）。 */
export type GatewayHealth = 'disabled' | 'noKey' | 'untested' | 'ok' | 'unavailable';

/** 取网关在列表中的唯一键（与后端 PUT/DELETE 的 (kind,name) 主键一致）。 */
export function gatewayKey(gw: Pick<ModelGateway, 'kind' | 'name'>): string {
  return `${gw.kind}:${gw.name}`;
}

/**
 * 计算单个网关的健康态。优先级：已禁用 → 无密钥 → 未检测 → 检测通过 / 当前不可用。
 * 「已禁用」「无密钥」是配置层面的确定性状态；后三者是运行层面的会话内状态。
 */
export function gatewayHealth(gw: ModelGateway, result?: GatewayTestResult): GatewayHealth {
  if (!gw.enabled) return 'disabled';
  if (!gw.hasKey) return 'noKey';
  if (!result) return 'untested';
  return result.ok ? 'ok' : 'unavailable';
}

/** 语音合成（或 LLM）服务的总体状态，用于页面顶部的可用性提示条。 */
export type ServiceState = 'unconfigured' | 'unavailable' | 'untested' | 'ready';

/**
 * 汇总某类型（tts/llm）服务的总体状态：
 * - `unconfigured`：本租户没有任何「启用 + 有密钥」的该类型网关（A23「未配置/模拟服务」的主场景）。
 * - `unavailable`：有可用配置，但会话内检测结果全部为失败（最近一次检测判定不可用）。
 * - `untested`：有可用配置，但本次会话尚未检测过（未检测 ≠ 不可用）。
 * - `ready`：至少一个网关会话内检测通过。
 */
export function serviceState(
  gateways: ModelGateway[],
  kind: 'tts' | 'llm',
  results: Record<string, GatewayTestResult>
): ServiceState {
  const usable = gateways.filter((gw) => gw.kind === kind && gw.enabled && gw.hasKey);
  if (usable.length === 0) return 'unconfigured';
  let sawOk = false;
  let sawUntested = false;
  for (const gw of usable) {
    const result = results[gatewayKey(gw)];
    if (!result) sawUntested = true;
    else if (result.ok) sawOk = true;
  }
  if (sawOk) return 'ready';
  if (sawUntested) return 'untested';
  return 'unavailable';
}

/** 健康态 → i18n 键后缀（`gateway.health<X>`）。 */
export const healthKeySuffix: Record<GatewayHealth, string> = {
  disabled: 'Disabled',
  noKey: 'NoKey',
  untested: 'Untested',
  ok: 'Ok',
  unavailable: 'Unavailable'
};

/** 总体服务态 → i18n 键后缀（`gateway.service<X>`）。 */
export const serviceKeySuffix: Record<ServiceState, string> = {
  unconfigured: 'Unconfigured',
  unavailable: 'Unavailable',
  untested: 'Untested',
  ready: 'Ready'
};
