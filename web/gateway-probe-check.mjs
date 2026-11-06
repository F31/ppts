#!/usr/bin/env node
/**
 * gateway-probe-check —— 「测试连接」失败时必须给出可行动的分类提示。
 *
 * 真实故障（2026-11-06）：平台共享 LLM 网关 local_llm 的配置确实保存在数据库里，
 * 凭据可解密、模型 ID 也在上游 /v1/models 中，但账户额度为负，flash 系列 chat
 * 静默挂到 30s 超时。旧页面只显示 "context deadline exceeded"，管理员会误判成
 * 「配置没写进库」而反复重录 Key。这里守住三件事：
 *   - 后端分类码 → 前端本地化文案的映射完整；
 *   - 没有分类码（旧后端 / 密钥不可读）必须退回原始错误，不能假装已诊断；
 *   - 源码级接线：后端写码、类型带码、页面用码，缺一环就等于没修。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/gatewayProbeError.ts');
const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');
const t = (key, params) => (params ? `${key}:${JSON.stringify(params)}` : key);

const CODES = {
  quota_exhausted: 'gateway.testFailQuota',
  credentials_rejected: 'gateway.testFailCredentials',
  model_not_in_catalog: 'gateway.testFailModel',
  rate_limited: 'gateway.testFailRateLimited',
  no_response: 'gateway.testFailNoResponse'
};

test('五类上游故障各有专属可行动文案', () => {
  for (const [code, key] of Object.entries(CODES)) {
    assert.equal(m.probeAdviceKey(code), key, code);
    assert.equal(m.probeHasSpecificAdvice(code), true, code);
    assert.equal(
      m.probeResultText({ ok: false, latencyMs: 0, error: 'raw', probeCode: code }, t),
      key
    );
  }
});

test('成功仍显示延迟', () => {
  assert.equal(
    m.probeResultText({ ok: true, latencyMs: 1145 }, t),
    'gateway.testOk:{"ms":1145}'
  );
});

test('无分类码/未知码退回原始错误，不吞掉真实故障', () => {
  const fallback = (code) =>
    m.probeResultText({ ok: false, latencyMs: 0, error: 'decrypt creds failed', probeCode: code }, t);
  assert.equal(m.probeAdviceKey(undefined), 'gateway.testFail');
  assert.equal(m.probeHasSpecificAdvice(undefined), false);
  assert.equal(m.probeHasSpecificAdvice('upstream_error'), false);
  assert.equal(fallback(undefined), 'gateway.testFail:{"error":"decrypt creds failed"}');
  assert.equal(fallback('upstream_error'), fallback(undefined));
  assert.equal(fallback('some_future_code'), fallback(undefined));
});

test('后端写分类码、类型带码、页面用码', () => {
  const handler = read('../internal/api/gateway.go');
  const providers = read('../internal/gateway/providers.go');
  const api = read('./src/api/gateway.ts');
  const page = read('./src/GatewaySettings.tsx');
  assert.match(handler, /resp\["probeCode"\] = gateway\.ProbeCodeOf\(probeErr\)/);
  assert.match(providers, /func ProbeCodeOf\(err error\) string/);
  assert.match(api, /probeCode\?: string/);
  assert.match(page, /probeResultText\(result, t\)/);
  assert.match(page, /probeHasSpecificAdvice\(result\.probeCode\)/);
  // 分类提示必须保留原文技术详情，不能只给一句笼统结论。
  assert.match(page, /<code>\{result\.error\}<\/code>/);
});

test('中英文案齐全且非空', () => {
  const zh = JSON.parse(read('./src/i18n/zh.json'));
  const en = JSON.parse(read('./src/i18n/en.json'));
  for (const key of [...Object.values(CODES), 'gateway.testFailDetail']) {
    assert.ok(typeof zh[key] === 'string' && zh[key].trim().length > 0, `zh 缺 ${key}`);
    assert.ok(typeof en[key] === 'string' && en[key].trim().length > 0, `en 缺 ${key}`);
  }
});
