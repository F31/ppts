#!/usr/bin/env node
/**
 * source-anchors-check —— 讲稿来源置信度分档门禁（G2-1 置信 UI）。
 *
 * 直接加载 src/sourceAnchors.ts（界面真正调用的那份），不另抄一份逻辑。
 * 盯的是「门禁看着绿、用户其实还是不知道哪句话是猜的」这几处：
 *   - 视觉通道的 0.85 上限不能被当成「确定」
 *   - 没标来源（0 / 缺失 / NaN）不能算确定——proto3 零值会被省略，前端拿到 undefined
 *   - 一条段落里只要有一条待核对，整段就要标出来
 *   - 函数写了但没接进界面，等于没做
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/sourceAnchors.ts');

test('结构通道的 1.0 才算确定', () => {
  assert.equal(m.anchorConfidenceLevel({ kind: 'shape_textbox', confidence: 1 }), 'trusted');
  assert.equal(m.anchorConfidenceLevel({ kind: 'notes', confidence: 1 }), 'trusted');
  assert.equal(m.anchorNeedsReview({ confidence: 1 }), false);
});

test('视觉通道的取值一律算推断，包括它的上限 0.85', () => {
  // 后端视觉通道缺省 0.7、clamp 到 ≤0.85；若阈值被写成 0.85，这一条会先红。
  assert.equal(m.anchorConfidenceLevel({ kind: 'visual_text', confidence: 0.7 }), 'inferred');
  assert.equal(m.anchorConfidenceLevel({ kind: 'visual_text', confidence: 0.85 }), 'inferred');
  assert.equal(m.anchorConfidenceLevel({ kind: 'visual_text', confidence: 0.99 }), 'trusted');
  assert.equal(m.anchorNeedsReview({ kind: 'visual_text', confidence: 0.85 }), true);
});

test('没有置信度就是「未标注」，不能默认当成确定', () => {
  assert.equal(m.anchorConfidenceLevel({ kind: 'shape_textbox' }), 'unverified');
  assert.equal(m.anchorConfidenceLevel({ confidence: 0 }), 'unverified');
  assert.equal(m.anchorConfidenceLevel({ confidence: Number.NaN }), 'unverified');
  assert.equal(m.anchorConfidenceLevel({ confidence: -1 }), 'unverified');
});

test('汇总按档计数，空输入不炸', () => {
  const summary = m.summarizeAnchors([
    { confidence: 1 },
    { confidence: 1 },
    { confidence: 0.7 },
    { kind: 'visual_chart' },
  ]);
  assert.deepEqual(summary, { total: 4, trusted: 2, needsReview: 2 });
  assert.deepEqual(m.summarizeAnchors([]), { total: 0, trusted: 0, needsReview: 0 });
  assert.deepEqual(m.summarizeAnchors(undefined), { total: 0, trusted: 0, needsReview: 0 });
  assert.deepEqual(m.summarizeAnchors(null), { total: 0, trusted: 0, needsReview: 0 });
});

test('段落里只要有一条待核对，整段就要标出来', () => {
  assert.equal(m.segmentNeedsReview({ sourceAnchors: [{ confidence: 1 }] }), false);
  assert.equal(m.segmentNeedsReview({ sourceAnchors: [{ confidence: 1 }, { confidence: 0.7 }] }), true);
  assert.equal(m.segmentNeedsReview({}), false, '没有锚点的段落不该被标黄');
});

test('提示原因优先报「未标注」，它比「模型推断」更没依据', () => {
  assert.equal(m.segmentReviewReason({ sourceAnchors: [{ confidence: 0.7 }, { confidence: 0 }] }), 'unverified');
  assert.equal(m.segmentReviewReason({ sourceAnchors: [{ confidence: 0.7 }] }), 'inferred');
  assert.equal(m.segmentReviewReason({ sourceAnchors: [{ confidence: 1 }] }), null);
  assert.equal(m.segmentReviewReason({}), null);
});

test('讲稿编辑器真的用了这套分档，而不是只写在模块里', () => {
  // 这是源码级接线断言（没有 DOM 测试框架），只为挡住「函数写了没调用」。
  const source = readFileSync(new URL('./src/ScriptEditor.tsx', import.meta.url), 'utf8');
  assert.match(source, /summarizeAnchors\(/, '顶部来源条必须调用 summarizeAnchors');
  assert.match(source, /segmentReviewReason\(/, '每段必须按原因判定是否待核对');
  assert.match(source, /anchor-review-badge/, '待核对徽标必须真的渲染出来');
});
