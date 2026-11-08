#!/usr/bin/env node
/**
 * animation-batch-check —— 批量套用动效的门禁。
 *
 * 守两件真会骗人的事：
 *   1. 后端说 applied=N，逐页结果里却只有 M 条 applied / 少给快照 → 界面
 *      不能显示「已套用」，撤销按钮也不能出现；
 *   2. 预检被拒时整批不写，前端必须把逐页原因说清楚，而不是只报一句
 *      「操作失败」让用户反复重试。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  batchTargets,
  batchApplyConsistent,
  summarizeBatchReasons,
  restorableSnapshots
} from './src/animationBatch.ts';

const reasonText = (r) => `理由:${r}`;

test('勾选集合去掉模板页、去重、去空', () => {
  assert.deepEqual(batchTargets(['s2', 's3', 's2', ' s4 ', ''], 's1'), ['s2', 's3', 's4']);
  assert.deepEqual(batchTargets(['s1', 's2'], 's1'), ['s2']);
  assert.deepEqual(batchTargets([], 's1'), []);
});

test('成功响应必须自洽：applied 与逐页结果、快照数一致', () => {
  const ok = {
    applied: 2,
    results: [
      { slideId: 's2', status: 'applied' },
      { slideId: 's3', status: 'applied' }
    ],
    previous: [{ slideId: 's2', existed: false }, { slideId: 's3', existed: true, revision: 1 }]
  };
  assert.equal(batchApplyConsistent(ok), true);
  // 汇总虚报
  assert.equal(batchApplyConsistent({ ...ok, applied: 3 }), false);
  // 少给一份快照 → 撤销会漏掉一页
  assert.equal(batchApplyConsistent({ ...ok, previous: ok.previous.slice(0, 1) }), false);
  // 响应压根没有 results / previous
  assert.equal(batchApplyConsistent({ applied: 2 }), false);
  assert.equal(batchApplyConsistent(null), false);
  // 0 页成功不能显示「已套用」
  assert.equal(batchApplyConsistent({ applied: 0, results: [], previous: [] }), false);
});

test('预检拒绝时逐页原因聚成一句可行动的清单', () => {
  const results = [
    { slideId: 's2', status: 'applied' },
    { slideId: 's3', status: 'skipped', reason: 'locked' },
    { slideId: 's4', status: 'skipped', reason: 'duration_unknown' },
    { slideId: 's5', status: 'skipped', reason: 'locked' }
  ];
  assert.equal(summarizeBatchReasons(results, reasonText), '理由:duration_unknown × 1、理由:locked × 2');
  assert.equal(summarizeBatchReasons(undefined, reasonText), '');
});

test('撤销前自检：快照不能为空、缺 ID 或重复', () => {
  const snaps = [{ slideId: 's2', existed: false }, { slideId: 's3', existed: true, revision: 1 }];
  assert.deepEqual(restorableSnapshots(snaps), snaps);
  assert.equal(restorableSnapshots([]), null);
  assert.equal(restorableSnapshots(null), null);
  assert.equal(restorableSnapshots([{ slideId: 's2' }, { slideId: 's2' }]), null);
  assert.equal(restorableSnapshots([{ slideId: '  ' }]), null);
});

test('前端必须复核成功响应、且只在拿到快照时开放撤销', () => {
  const src = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
  assert.match(src, /batchApplyConsistent\(result\)/, '不能只信后端汇总数字');
  assert.match(src, /restorableSnapshots\(result\.previous\)/, '撤销快照必须自检');
  assert.match(src, /code === 'batch_ineligible'/, '预检拒绝必须单独说明逐页原因');
  assert.match(src, /!batchUndo \|\| batchBusy/, '没有快照时撤销按钮必须禁用');
});

test('批量套用的错误文案必须用已登记的 i18n 键', () => {
  const src = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
  const zh = JSON.parse(readFileSync(new URL('./src/i18n/zh.json', import.meta.url), 'utf8'));
  const en = JSON.parse(readFileSync(new URL('./src/i18n/en.json', import.meta.url), 'utf8'));
  // i18n-check 对「源码引用了不存在的 key」只告警不阻塞；批量套用这里是自己引入的洞：
  // 兜底文案用了一个不存在的 common.error，界面会原样把 key 显示给用户。
  assert.doesNotMatch(src, /t\('common\.error'\)/, '不存在的兜底键会让界面原样显示 key');
  const keys = [...src.matchAll(/t\('(editor\.animationBatch[^']+)'/g)].map((m) => m[1]);
  assert.ok(keys.length >= 10, '批量套用文案键应当成组登记');
  for (const key of keys) {
    assert.ok(zh[key], `zh.json 缺 ${key}`);
    assert.ok(en[key], `en.json 缺 ${key}`);
  }
});

test('后端两条批量路由都注册了', () => {
  const src = readFileSync(new URL('../internal/api/animation.go', import.meta.url), 'utf8');
  assert.match(src, /animation\/apply-batch"/);
  assert.match(src, /animation\/apply-batch\/restore"/);
});
