// 模型服务「测试连接」的失败分类展示（纯函数，供门禁直接加载）。
//
// 真实故障（2026-11-06，平台共享 LLM 网关 local_llm）：配置确实保存在数据库里，
// 凭据可解密、模型 ID 也在上游 /v1/models 里，但账户额度为负，flash 系列 chat
// 请求静默挂到 30s 超时。旧页面只显示 "context deadline exceeded"，管理员会
// 误以为「配置没写进库」而反复重录 Key。后端现在给出分类码，这里把它翻成
// 可行动提示，同时保留原始错误做技术详情，不把故障藏起来。
import type { GatewayTestResult } from './api/gateway';

const ADVICE_KEYS: Record<string, string> = {
  quota_exhausted: 'gateway.testFailQuota',
  credentials_rejected: 'gateway.testFailCredentials',
  model_not_in_catalog: 'gateway.testFailModel',
  rate_limited: 'gateway.testFailRateLimited',
  no_response: 'gateway.testFailNoResponse'
};

type Translate = (key: string, params?: Record<string, string | number>) => string;

export function probeAdviceKey(code?: string): string {
  return ADVICE_KEYS[code ?? ''] ?? 'gateway.testFail';
}

export function probeHasSpecificAdvice(code?: string): boolean {
  return Object.hasOwn(ADVICE_KEYS, code ?? '');
}

export function probeResultText(result: GatewayTestResult, t: Translate): string {
  if (result.ok) return t('gateway.testOk', { ms: result.latencyMs });
  const key = probeAdviceKey(result.probeCode);
  // 没有分类码（旧后端、密钥不可读等）时退回原文，避免把真实错误吞掉。
  if (key === 'gateway.testFail') return t('gateway.testFail', { error: result.error ?? '' });
  return t(key);
}
