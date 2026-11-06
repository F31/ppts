#!/usr/bin/env node
/**
 * gateway-share-check —— 「平台共享的模型服务」在租户侧是否看得见、且不可误改。
 *
 * 修复前的真实行为：运行时 Resolve/ResolveModel 在租户没有默认行时会回落到平台默认行，
 * 但管理面列表只查本租户，于是平台正在给这个租户供 TTS，界面却显示「本租户尚未接入
 * 可用的语音合成供应商」——用户被明确告知"没配"，实际上是配了的。
 *
 * 这里守两件事：
 *   - 来源判定：source=platform 才是只读；缺字段（旧后端/单条响应）必须按租户处理，
 *     否则升级瞬间所有人的配置都变成"改不了"。
 *   - 状态判定：平台行必须计入服务可用性，不能再报 unconfigured。
 * 另外做源码级接线断言：函数存在但列表没用它，等于没修。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/gatewayState.ts');

const tts = (over = {}) => ({
  tenantId: '00000000-0000-0000-0000-000000000000',
  name: 'plat-tts',
  kind: 'tts',
  provider: 'openai_compatible',
  baseUrl: 'https://platform.example.com',
  model: 'cosyvoice',
  visionModel: '',
  voice: '',
  sampleRate: 0,
  isDefault: true,
  enabled: true,
  version: 1,
  hasKey: true,
  keyMasked: 'sk-****123',
  createdAt: 0,
  updatedAt: 0,
  ...over
});

test('source=platform 才是只读', () => {
  assert.equal(m.isPlatformShared({ source: 'platform' }), true);
  assert.equal(m.isPlatformShared({ source: 'tenant' }), false);
});

test('缺 source 时按本租户处理（旧后端不能被误判成只读）', () => {
  assert.equal(m.isPlatformShared({}), false);
  assert.equal(m.isPlatformShared(tts()), false);
});

test('只有平台共享配置时，不能报「未配置」', () => {
  // 这是修复前最直接的误导：运行时明明在用平台 TTS，横幅却说本租户没接入。
  assert.equal(m.serviceState([tts({ source: 'platform' })], 'tts', {}), 'untested');
});

test('平台行参与可用性汇总：检测通过即 ready', () => {
  const gw = tts({ source: 'platform' });
  assert.equal(m.serviceState([gw], 'tts', { [m.gatewayKey(gw)]: { ok: true, latencyMs: 12 } }), 'ready');
});

test('平台行被禁用或没有密钥时仍然算未配置', () => {
  assert.equal(m.serviceState([tts({ source: 'platform', enabled: false })], 'tts', {}), 'unconfigured');
  assert.equal(m.serviceState([tts({ source: 'platform', hasKey: false })], 'tts', {}), 'unconfigured');
});

test('真的一条都没有时仍是 unconfigured', () => {
  assert.equal(m.serviceState([], 'tts', {}), 'unconfigured');
});

test('列表真的用了来源判定，且平台行不渲染编辑/删除入口，但保留测试连接', () => {
  const src = readFileSync(new URL('./src/GatewaySettings.tsx', import.meta.url), 'utf8');
  assert.match(src, /isPlatformShared\(gw\)/, '列表项必须判定来源');
  assert.match(src, /shared \? \(/, '平台行必须走只读分支');
  assert.match(src, /t\('gateway\.platformReadOnly'\)/, '平台行必须给出只读原因');
  // 三个写入口都在 false 分支（可编辑行）里，平台行只走 `shared ? (...)` 的只读分支。
  const start = src.indexOf('{shared ? (');
  const elseAt = src.indexOf(') : (', start);
  assert.ok(start > 0 && elseAt > start, '找不到平台行的只读分支');
  const sharedBranch = src.slice(start, elseAt);
  for (const marker of ['startEdit(gw)', 'remove(gw)', 'setDefaultGateway(identity']) {
    assert.ok(!sharedBranch.includes(marker), `只读分支里不应出现写入口：${marker}`);
  }
  // 只读 ≠ 不可验证：平台共享行必须能点「测试连接」，否则租户只能盲信平台配置。
  assert.match(sharedBranch, /runTest\(gw\)/, '平台行必须有测试连接入口');
  assert.match(src, /isPlatformShared\(gw\) \? 'platform' : undefined/, '测试平台行必须带 source=platform');
});

test('密钥不可读不能被误报成未配置密钥', () => {
  const gw = tts({ keyUnreadable: true });
  assert.equal(m.gatewayHealth(gw), 'unreadable');
  assert.equal(m.serviceState([gw], 'tts', {}), 'unreadable');
  assert.equal(m.inheritedOnly([gw], 'tts'), false);
  // 真的没配 key 仍是 unconfigured，两者不能混。
  assert.equal(m.gatewayHealth(tts({ hasKey: false })), 'noKey');
  assert.equal(m.serviceState([tts({ hasKey: false })], 'tts', {}), 'unconfigured');
});

test('全靠平台共享时仍可自行测试连接', () => {
  assert.equal(m.inheritedOnly([tts({ source: 'platform' })], 'tts'), true);
});

test('本租户也有可用配置时不算「全靠共享」', () => {
  assert.equal(m.inheritedOnly([tts({ source: 'platform' }), tts({ source: 'tenant', name: 'own' })], 'tts'), false);
  assert.equal(m.inheritedOnly([tts({ source: 'tenant' })], 'tts'), false);
  assert.equal(m.inheritedOnly([], 'tts'), false);
});

test('横幅真的按「全靠共享」切换文案', () => {
  const src = readFileSync(new URL('./src/GatewaySettings.tsx', import.meta.url), 'utf8');
  assert.match(src, /inheritedOnly\(gateways, 'tts'\)/, '横幅必须区分「全靠共享」');
  assert.match(src, /t\('gateway\.serviceInherited'\)/, '全靠共享时要用专用文案');
});

// 本地语音模型（Qwen3-TTS）不校验 API Key：管理面不能因为 hasKey=false
// 就把「已可用」的本机服务标成「未配置 key / 服务未接入」。
test('本机回环 HTTP 网关免 key 仍按真实探活判定', () => {
  for (const url of ['http://127.0.0.1:8000', 'http://localhost:18080/v1', 'http://127.0.0.2:8000']) {
    assert.equal(m.isLoopbackBaseURL(url), true, `${url} 应判为本机`);
  }
  for (const url of ['https://127.0.0.1:8000', 'https://api.siliconflow.cn', 'http://192.168.1.5:8000', '', 'not a url']) {
    assert.equal(m.isLoopbackBaseURL(url), false, `${url} 不应判为本机`);
  }
  const local = tts({ hasKey: false, keyMasked: '', baseUrl: 'http://127.0.0.1:8000' });
  assert.equal(m.hasUsableCredential(local), true);
  assert.equal(m.gatewayHealth(local), 'untested');
  assert.equal(m.gatewayHealth(local, { ok: true, latencyMs: 12 }), 'ok');
  assert.equal(m.gatewayHealth(local, { ok: false, latencyMs: 12 }), 'unavailable');
  assert.equal(m.serviceState([local], 'tts', {}), 'untested');
  assert.equal(m.serviceState([local], 'tts', { 'tts:plat-tts': { ok: true, latencyMs: 12 } }), 'ready');
  assert.equal(m.inheritedOnly([tts({ source: 'platform', hasKey: false, keyMasked: '', baseUrl: 'http://127.0.0.1:8000' })], 'tts'), true);
  // 远程无 key 仍是「未配置」，不能被放宽成免鉴权。
  assert.equal(m.gatewayHealth(tts({ hasKey: false })), 'noKey');
  assert.equal(m.serviceState([tts({ hasKey: false })], 'tts', {}), 'unconfigured');
});

test('管理面真的区分「本机免鉴权」与「未配置 key」', () => {
  const src = readFileSync(new URL('./src/GatewaySettings.tsx', import.meta.url), 'utf8');
  assert.match(src, /isLoopbackBaseURL\(gw\.baseUrl\) \? t\('gateway\.localNoAuth'\)/, '空 key 的本机行要显示免鉴权文案');
  assert.match(src, /hasUsableCredential\(gw\)/, '未检测提示要覆盖免鉴权网关');
});
