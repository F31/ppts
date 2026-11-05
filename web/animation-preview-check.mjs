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

const t = (key) => key;

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

// ─── 2026-11-05 回归：预览按钮不能再「点了才知道必然失败」 ─────────────────
//
// 部署没配渲染服务时，预览路由原本不注册，POST 返回 404 纯文本；前端错误映射
// 又没有 http_404，于是用户只看到「预览失败」四个字，反复重试一个永远不可能
// 成功的按钮。下面钉住两件事：按钮在点之前就说清原因，失败时也不再吞信息。

const button = (overrides = {}) => m.previewButtonState({
  capability: { configured: true, reason: 'ok' },
  capabilityState: 'ready',
  hasTimeline: true,
  hasSavedSpec: true,
  previewing: false,
  panelDisabled: false,
  ...overrides
});

test('能力探测在途时按钮禁用且说明正在检测', () => {
  assert.deepEqual(button({ capabilityState: 'checking', capability: null }), {
    disabled: true,
    hintKey: 'editor.animationPreviewChecking'
  });
});

test('能力探测失败时按钮禁用并给重试前提', () => {
  assert.deepEqual(button({ capabilityState: 'error', capability: null }), {
    disabled: true,
    hintKey: 'editor.animationPreviewCapabilityFailed'
  });
});

test('部署未配置渲染服务：按钮禁用，理由指向具体配置项', () => {
  const state = button({
    capability: { configured: false, reason: 'renderer_not_configured' }
  });
  assert.equal(state.disabled, true);
  assert.equal(state.hintKey, 'editor.animationPreviewUnconfiguredRenderer');
});

test('部署级原因优先于「没有配音」：不该让用户白跑一次配音生成', () => {
  const state = button({
    capability: { configured: false, reason: 'assets_not_configured' },
    hasTimeline: false
  });
  assert.equal(state.hintKey, 'editor.animationPreviewUnconfiguredAssets');
});

test('配置齐全时按时间轴 / 已保存规格依次判断，全齐才允许点', () => {
  assert.equal(button({ hasTimeline: false }).hintKey, 'editor.animationPreviewNeedsVoice');
  assert.equal(button({ hasSavedSpec: false }).hintKey, 'editor.animationPreviewNeedsSave');
  assert.equal(button({ previewing: true }).disabled, true);
  assert.deepEqual(button(), { disabled: false, hintKey: '' });
});

test('404 必须说「接口不存在」，不能落到那句没有原因的「预览失败」', () => {
  assert.equal(
    m.previewErrorText({ code: 'http_404' }, t),
    'editor.animationPreviewEndpointMissing'
  );
});

test('未启用的 POST 与按钮置灰说同一句话（reason 一致）', () => {
  const hint = button({ capability: { configured: false, reason: 'animation_disabled' } }).hintKey;
  const text = m.previewErrorText({ code: 'preview_unavailable', reason: 'animation_disabled' }, t);
  assert.equal(text, hint);
});

test('非视频响应 / 空响应单独归因，不再把 HTML 塞进 <video> 后静默黑屏', () => {
  assert.equal(
    m.previewErrorText({ code: 'unexpected_content_type' }, t),
    'editor.animationPreviewResponseNotVideo'
  );
  assert.equal(
    m.previewErrorText({ code: 'preview_empty_body' }, t),
    'editor.animationPreviewResponseNotVideo'
  );
});

test('未登记的 code 不再吞掉服务端 message', () => {
  assert.equal(
    m.previewErrorText({ code: 'something_new', message: 'renderer: job failed' }, t),
    'editor.animationPreviewFailed: renderer: job failed'
  );
  assert.equal(
    m.previewErrorText({ code: 'something_new' }, t),
    'editor.animationPreviewFailed'
  );
});

test('技术详情带上 code / reason / message，便于反馈给运维', () => {
  const detail = m.previewErrorDetail({
    code: 'preview_unavailable',
    reason: 'renderer_not_configured',
    message: 'missing PPTS_RENDERER_URL'
  });
  assert.match(detail, /code: preview_unavailable/);
  assert.match(detail, /reason: renderer_not_configured/);
  assert.match(detail, /missing PPTS_RENDERER_URL/);
});
