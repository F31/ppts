#!/usr/bin/env node
/**
 * one-click-check —— 一键成片「讲稿步能不能继续」的门禁。
 *
 * 守的是一条真机故障：job.state=succeeded，但逐页 12 failed / 3 degraded，
 * 旧代码照样拿半成品去配音，最后界面报「完成」。这里要求：
 *   1. 讲稿页数只能来自 stepCounts.success，不能用请求的页数冒充；
 *   2. 失败 / 降级一律拦住，降级是「原文兜底」不是成稿成功；
 *   3. 读不到步骤（stepsError / 空步骤 / detail 缺失）必须显式报错，不能当成没问题。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/oneClickJob.ts');
const plan = await import('./src/pages/projectEditor/oneClickPlan.ts');

const detail = (stepCounts, extra = {}) => ({
  jobId: 'j1',
  kind: 'script_draft',
  traceId: '',
  scope: { kind: 'project', pageCount: 1, affectedPages: [], inputRevision: 1 },
  steps: [],
  stepCounts,
  stepTotal: Object.values(stepCounts).reduce((a, b) => (typeof b === 'number' ? a + b : a), 0),
  stepsTruncated: false,
  ...extra
});

test('全部成功：可继续，页数取真实 success 而不是请求的页数', () => {
  const out = m.evaluateDraftJob(detail({ success: 7, skipped: 1 }));
  assert.equal(out.ok, true);
  assert.equal(out.generated, 7);
  assert.equal(out.skipped, 1);
  assert.equal(out.errorKey, '');
});

test('有失败页：必须拦住，并带上首个逐页原因', () => {
  const d = detail({ success: 7, failed: 12, degraded: 3, skipped: 1 });
  d.steps = [
    { stepType: 'page', state: 'success', updatedAtUnix: 0, hasResult: false },
    { stepType: 'page', state: 'failed', updatedAtUnix: 0, hasResult: false, reason: 'LLM 超时' }
  ];
  const out = m.evaluateDraftJob(d);
  assert.equal(out.ok, false);
  assert.equal(out.failed, 12);
  assert.equal(out.degraded, 3);
  assert.equal(out.errorKey, 'editor.oneClickDraftIncompleteWithReason');
  assert.equal(out.params.reason, 'LLM 超时');
});

test('只有降级页也要拦住：降级是原文兜底，不是成稿成功', () => {
  const out = m.evaluateDraftJob(detail({ success: 10, degraded: 2 }));
  assert.equal(out.ok, false);
  assert.equal(out.errorKey, 'editor.oneClickDraftIncomplete');
  assert.equal(out.params.failed, 0);
  assert.equal(out.params.degraded, 2);
  assert.equal(out.params.generated, 10);
});

// 真机 2026-11-06：润色讲解 + 覆盖全部真实跑出 {success:20, degraded:2, skipped:1}/23。
// 旧文案只报「0 页失败、2 页降级」，用户会以为整份白跑；必须同时说清已成功 20 页。
test('真机降级场景：拦截文案如实带上成功页数与首个原因', () => {
  const d = detail({ success: 20, degraded: 2, skipped: 1 });
  d.steps = [
    { stepType: 'page', state: 'degraded', updatedAtUnix: 0, hasResult: false, reason: '第一版新增「数字 1」；修补版含内部标记或思考过程' }
  ];
  const out = m.evaluateDraftJob(d);
  assert.equal(out.ok, false);
  assert.equal(out.errorKey, 'editor.oneClickDraftIncompleteWithReason');
  assert.equal(out.params.generated, 20);
  assert.equal(out.params.failed, 0);
  assert.equal(out.params.degraded, 2);
  const zh = JSON.parse(readFileSync(new URL('./src/i18n/zh.json', import.meta.url), 'utf8'));
  const text = String(zh[out.errorKey]).replace(/\{(\w+)\}/g, (_, k) => String(out.params[k] ?? ''));
  assert.match(text, /已成功生成 20 页/);
  assert.match(text, /0 页失败、2 页降级/);
  assert.match(text, /复核降级页/);
  assert.match(text, /第一版新增/);
});

test('stepsError / 空步骤 / detail 缺失都判为无法核验，不放行', () => {
  const unreadable = m.evaluateDraftJob(detail({ success: 0 }, { stepsError: 'load_failed' }));
  assert.equal(unreadable.ok, false);
  assert.equal(unreadable.errorKey, 'editor.oneClickDraftUnverifiable');
  assert.equal(unreadable.params.error, 'load_failed');

  const empty = m.evaluateDraftJob(detail({}));
  assert.equal(empty.ok, false);
  assert.equal(empty.params.error, 'no_steps');

  const missing = m.evaluateDraftJob(null);
  assert.equal(missing.ok, false);
  assert.equal(missing.params.error, 'missing_job_detail');
});

test('异常计数（NaN / 负数）按 0 处理，不把脏数据当成成功', () => {
  const out = m.evaluateDraftJob(
    detail({ success: Number.NaN, failed: -3, degraded: Number.NaN }),
    { stepTotal: 23 }
  );
  assert.equal(out.generated, 0);
  assert.equal(out.failed, 0);
  assert.equal(out.degraded, 0);
  // stepTotal 有值但没有任何成功页：仍不能报「完成 0 页」后继续配音。
  assert.equal(out.ok, false);
  assert.equal(out.errorKey, 'editor.oneClickDraftUnverifiable');
  assert.equal(out.params.error, 'no_generated_pages');
});

test('超长逐页原因要截断，避免把整段模型报错糊到界面上', () => {
  const d = detail({ failed: 1 });
  d.steps = [
    { stepType: 'page', state: 'failed', updatedAtUnix: 0, hasResult: false, reason: 'x'.repeat(400) }
  ];
  const out = m.evaluateDraftJob(d);
  assert.equal(out.errorKey, 'editor.oneClickDraftIncompleteWithReason');
  assert.equal(String(out.params.reason).length, 161);
  assert.ok(String(out.params.reason).endsWith('…'));
});

// ─── 2026-11-06 真机回归：勾选任意一步曾让整页崩溃 ─────────────────────────
// 三个复选框写成 setOneClickSteps((s) => ({ ...s, x: e.currentTarget.checked }))。
// React 19 在事件回调结束后把合成事件的 currentTarget 重置为 null，而 updater
// 可能延迟执行，于是抛 "Cannot read properties of null (reading 'checked')"，
// ErrorBoundary 接管整页。CDP 实点第 2/3 个复选框都复现。
test('全部跳过：如实说「没有生成新讲稿」，不冒充无法核验', () => {
  const out = m.evaluateDraftJob(detail({ skipped: 23 }));
  assert.equal(out.ok, false);
  assert.equal(out.errorKey, 'editor.oneClickDraftAllSkipped');
  assert.equal(out.params.skipped, 23);
});

test('状态更新函数里不得再读 e.currentTarget（否则整页崩溃）', () => {
  // 复选框已搬进独立抽屉组件；约束仍覆盖页面与组件两处，避免有人把旧写法加回来。
  const pageSrc = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
  const drawerSrc = readFileSync(new URL('./src/pages/projectEditor/OneClickDrawer.tsx', import.meta.url), 'utf8');
  const unsafe = /(?:setOneClickSteps|onStepsChange)\(\s*\(s\)\s*=>\s*\(\{[^}]*e\.currentTarget/g;
  for (const [name, src] of [['页面', pageSrc], ['抽屉', drawerSrc]]) {
    assert.equal(
      unsafe.test(src),
      false,
      `${name}：setState 的更新函数里读 e.currentTarget 会在 React 19 下崩溃；必须先同步取出 checked`
    );
  }
  // 修复形态必须保留：三个复选框各自先取值。
  const safeAssignments = (pageSrc + drawerSrc).match(/const checked = e\.currentTarget\.checked;/g) || [];
  assert.equal(safeAssignments.length, 3, '三个一键成片复选框都要先同步取出 checked');
});

// 真机反向验证踩过的坑：一键成片抽屉能选「润色讲解」，覆盖策略却藏在
// 另一个抽屉里且默认「仅补空」，已有讲稿时 23 页全部 skipped，用户以为
// 换了生成方式就会重新润色。覆盖策略必须在本抽屉可见并真正传给流水线。
test('一键成片抽屉可直接改覆盖策略，且流水线真的用这个值', () => {
  const src = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
  const drawer = readFileSync(new URL('./src/pages/projectEditor/OneClickDrawer.tsx', import.meta.url), 'utf8');
  assert.match(drawer, /value=\{overwrite\}/, '覆盖策略下拉要在本抽屉里');
  assert.match(drawer, /editor\.oneClickOverwriteNote/, '抽屉里要说明覆盖策略的影响');
  assert.match(src, /overwrite=\{oneDraftOverwrite\}/, '页面必须把覆盖策略传给抽屉');
  assert.match(src, /overwrite: oneDraftOverwrite === 'overwrite_all'/, '流水线必须真的用这个值');
});

// 成稿预估（跳过/无可生成材料）是从编辑器里抽出来的纯函数，口径必须守住：
// 它给的是"估算"，但只要口径错了，用户会在点下成稿前就做出错误决定。
const slide = (id, preview, hasNotes) => ({ slideId: id, preview, hasNotes });
const scriptOf = (id) => ({ slideId: id, segments: [{ segmentId: 's1' }] });

test('notes_only：有备注即可成稿，preview 不参与判据', () => {
  const out = plan.computeOneDraftPlan({
    slides: [slide('a', '', true), slide('b', '有文字但没备注', false), slide('c', '  ', false)],
    scripts: {},
    source: 'notes_only',
    overwrite: 'fill_empty',
    language: 'zh-CN',
    panelLanguage: 'zh-CN'
  });
  assert.equal(out.generate, 1);
  assert.equal(out.noText, 2);
  assert.equal(out.withNotes, 1);
  assert.equal(out.total, 3);
});

test('page_content + 仅补空：已有讲稿的页算跳过，不算生成', () => {
  const out = plan.computeOneDraftPlan({
    slides: [slide('a', '第一页文字', false), slide('b', '第二页文字', true)],
    scripts: { a: scriptOf('a') },
    source: 'page_content',
    overwrite: 'fill_empty',
    language: 'zh-CN',
    panelLanguage: 'zh-CN'
  });
  assert.equal(out.generate, 1);
  assert.equal(out.skippedExisting, 1);
  assert.equal(out.reliable, true);
});

test('覆盖全部：已有讲稿也要重算，不产生跳过', () => {
  const out = plan.computeOneDraftPlan({
    slides: [slide('a', '第一页文字', false), slide('b', '第二页文字', true)],
    scripts: { a: scriptOf('a'), b: scriptOf('b') },
    source: 'page_content',
    overwrite: 'overwrite_all',
    language: 'zh-CN',
    panelLanguage: 'zh-CN'
  });
  assert.equal(out.generate, 2);
  assert.equal(out.skippedExisting, 0);
});

test('生成语言与面板语言不一致时，跳过数只能标为估算', () => {
  const base = {
    slides: [slide('a', '文字', false)],
    scripts: {},
    source: 'page_content',
    overwrite: 'fill_empty',
    language: 'en-US'
  };
  assert.equal(plan.computeOneDraftPlan({ ...base, panelLanguage: '' }).reliable, false);
  assert.equal(plan.computeOneDraftPlan({ ...base, panelLanguage: 'zh-CN' }).reliable, false);
  assert.equal(plan.computeOneDraftPlan({ ...base, panelLanguage: 'en-US' }).reliable, true);
});
