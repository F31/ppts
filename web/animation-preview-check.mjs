#!/usr/bin/env node
/**
 * animation-preview-check —— 动效预览下方「焦点说明」的文案选择门禁。
 *
 * 守的是**跳过原因不能说错**：
 *   - 比例问题说成「目标找不到」，用户会去改讲稿里选的那根柱子，而那儿没错；
 *   - 未知原因硬塞进已知分支，等于在猜；
 *   - 没有焦点时不要冒出一行「0 处焦点」。
 *
 * 这几条都不会报错：预览照常播放、接口 200、日志干净，只是提示指向了错的地方。
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const m = await import('./src/animationPreview.ts');

const preview = (focus, focusSkipped, focusSkipReason = '') => ({
  focus,
  focusSkipped,
  focusSkipReason
});

test('有焦点且没有跳过时只说一句', () => {
  const out = m.previewFocusNotices(preview(2, 0));
  assert.equal(out.length, 1);
  assert.equal(out[0].key, 'editor.animationPreviewFocus');
  assert.deepEqual(out[0].params, { count: 2 });
});

test('完全没写焦点时不打扰用户', () => {
  assert.deepEqual(m.previewFocusNotices(preview(0, 0)), []);
});

test('比例原因必须说成比例，不能混同于「目标找不到」', () => {
  const out = m.previewFocusNotices(preview(1, 2, 'canvas_aspect'));
  const texts = out.map((n) => n.key);
  assert.deepEqual(texts, ['editor.animationPreviewFocus', 'editor.animationPreviewFocusSkippedAspect']);
  assert.deepEqual(out[1].params, { count: 2 });
});

test('目标缺失说成目标缺失（生产发的是诊断码，不是简称）', () => {
  // 服务端把 render.DiagFocusTargetMissing 原样放进 X-Preview-Focus-Skip-Reason，
  // 也就是 'focus_target_missing'。这条用例以前喂的是简称 'target_missing'——
  // 于是「精确文案」这一支在生产上从未命中，而测试全绿。
  const real = m.previewFocusNotices(preview(0, 1, 'focus_target_missing'));
  assert.deepEqual(real.map((n) => n.key), ['editor.animationPreviewFocusSkippedMissing']);
  // 简称仍认：旧服务端/旧链接上可能还带着它。
  const legacy = m.previewFocusNotices(preview(0, 1, 'target_missing'));
  assert.deepEqual(legacy.map((n) => n.key), ['editor.animationPreviewFocusSkippedMissing']);
});

test('未知原因给通用文案，不冒充已知分支', () => {
  const out = m.previewFocusNotices(preview(0, 3, 'something_new'));
  assert.deepEqual(out.map((n) => n.key), ['editor.animationPreviewFocusSkipped']);
});
