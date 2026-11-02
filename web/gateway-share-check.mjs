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

test('列表真的用了来源判定，且平台行不渲染编辑/删除入口', () => {
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
});
