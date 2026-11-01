#!/usr/bin/env node
/**
 * voice-preview-check —— 音色试听的时序与错误归类门禁。
 *
 * 直接加载 src/voicePreview.ts（组件真正调用的那份），不另抄一份逻辑。
 * 重点在三条用户能立刻感觉到、但手点很难复现的地方：
 *   - 改音色后，上一次试听的音频不能播到新音色上（串音）
 *   - 关掉面板后，在途响应不能再触发播放
 *   - 四种失败的提示必须分开（去配模型 / 等会儿 / 改短 / 其它）
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const m = await import('./src/voicePreview.ts');

test('请求键随音色 / 语速 / 文本变化，仅尾随空格不算变化', () => {
  const base = { voice: 'v1', ratePercent: 100 };
  assert.equal(m.voicePreviewKey(base), m.voicePreviewKey({ ...base, text: '' }));
  assert.equal(m.voicePreviewKey({ ...base, text: '你好' }), m.voicePreviewKey({ ...base, text: '  你好  ' }));
  assert.notEqual(m.voicePreviewKey(base), m.voicePreviewKey({ ...base, voice: 'v2' }));
  assert.notEqual(m.voicePreviewKey(base), m.voicePreviewKey({ ...base, ratePercent: 130 }));
  assert.notEqual(m.voicePreviewKey({ ...base, text: '甲' }), m.voicePreviewKey({ ...base, text: '乙' }));
});

test('后发起的试听让先前的 token 失效（防止 A 的音频落到 B 上）', () => {
  const seq = new m.VoicePreviewSequencer();
  const tokenA = seq.begin('v1|100|');
  assert.equal(seq.isCurrent(tokenA, 'v1|100|'), true);

  const tokenB = seq.begin('v2|100|');
  assert.equal(seq.isCurrent(tokenA, 'v1|100|'), false, 'A 已在 B 发起后过期');
  assert.equal(seq.isCurrent(tokenB, 'v2|100|'), true);
});

test('参数变了但没重新发起时，旧 token 也不再生效', () => {
  const seq = new m.VoicePreviewSequencer();
  const token = seq.begin('v1|100|');
  assert.equal(seq.isCurrent(token, 'v1|130|'), false, '语速已改，旧试听结果不能代表当前参数');
});

test('取消后任何在途响应都不落地', () => {
  const seq = new m.VoicePreviewSequencer();
  const token = seq.begin('v1|100|');
  seq.cancel();
  assert.equal(seq.isCurrent(token, 'v1|100|'), false);
  assert.equal(seq.isCurrent(token, ''), false);
});

test('四次连续发起只有最后一次算数', () => {
  const seq = new m.VoicePreviewSequencer();
  const tokens = ['a', 'b', 'c', 'd'].map((k) => seq.begin(k));
  const alive = tokens.map((token, i) => seq.isCurrent(token, 'abcd'[i]));
  assert.deepEqual(alive, [false, false, false, true]);
});

test('错误码归类：五种情况对应五种可操作结论', () => {
  assert.equal(m.classifyVoicePreviewError('tts_not_configured'), 'not_configured');
  // 模型行被停用/删除：用户该做的是重选模型，而不是去查供应商。
  assert.equal(m.classifyVoicePreviewError('model_not_configured'), 'model_missing');
  assert.equal(m.classifyVoicePreviewError('tts_timeout'), 'timeout');
  assert.equal(m.classifyVoicePreviewError('text_too_long'), 'too_long');
  assert.equal(m.classifyVoicePreviewError('tts_failed'), 'failed');
  // 未知码不能落到"未配置"上：那会把故障说成"平台没这个能力"。
  assert.equal(m.classifyVoicePreviewError('http_500'), 'failed');
  assert.equal(m.classifyVoicePreviewError(undefined), 'failed');
});
