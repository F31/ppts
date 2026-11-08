#!/usr/bin/env node
/**
 * tts-model-check —— 「语音模型」下拉是否真的影响合成结果。
 *
 * 修复前的真实行为：下拉只过滤音色列表，生成请求里根本没有模型，
 * 后端一直用租户默认网关注合成。界面上换模型"保存成功"，声音不变，
 * 而且没有任何一条日志说它忽略了选择——这类假成功最难被发现。
 *
 * 所以这里既测纯函数（请求身份必须含模型），也做源码级接线断言：
 * 光有函数不算生效，必须真的被调用、真的进了请求/请求头。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/voicePreview.ts');

const voiceAPI = readFileSync(new URL('./src/api/voice.ts', import.meta.url), 'utf8');
const narrationAPI = readFileSync(new URL('./src/api/narration.ts', import.meta.url), 'utf8');
const editor = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
// 试听的请求构造与样音清理已抽到 hook；断言跟随搬家，约束不变。
const previewHook = readFileSync(new URL('./src/pages/projectEditor/useVoicePreview.ts', import.meta.url), 'utf8');

test('试听请求键含模型：换模型后旧样音不能落到新选择上', () => {
  const base = { voice: 'v1', ratePercent: 100 };
  assert.notEqual(
    m.voicePreviewKey({ ...base, model: 'model-a' }),
    m.voicePreviewKey({ ...base, model: 'model-b' }),
    '模型不同但音色名相同时，键必须不同'
  );
  // 未指定模型（旧调用方）仍要能算出稳定的键。
  assert.equal(m.voicePreviewKey({ ...base, model: '' }), m.voicePreviewKey(base));
});

test('试听请求体真的把模型发出去', () => {
  assert.match(
    voiceAPI,
    /body:\s*\{\s*voice:\s*body\.voice,\s*model:\s*body\.model/,
    'previewVoice 必须把 model 放进请求体，否则试听用的还是默认模型'
  );
});

test('生成请求把模型放进 X-PPTS-TTS-Model 头', () => {
  assert.match(
    narrationAPI,
    /headers\['X-PPTS-TTS-Model'\]\s*=\s*opts\.model/,
    'createGeneration 必须把 model 写进请求头（proto 无 model 字段，走自定义头）'
  );
  // 空模型不得写成空头：后端把"空"当"未指定"，发一个空字符串只是噪声。
  assert.match(
    narrationAPI,
    /if\s*\(opts\.model\)\s*headers\['X-PPTS-TTS-Model'\]/,
    '只在 model 非空时写头'
  );
});

test('编辑器生成路径传的是「本次生效」的模型', () => {
  assert.match(editor, /model:\s*selectedModel/, 'createGeneration 调用必须带 model');
  // selectedModel 必须 override 优先：弹窗刚保存的模型是异步 setState，
  // 直接读 state 会丢掉这一次选择。
  assert.match(
    editor,
    /const selectedModel = override\.model \?\? voiceModelName/,
    'selectedModel 需要 override 优先，不能只读 state'
  );
  assert.match(
    editor,
    /model:\s*saved\.model \|\| voiceDraftModel/,
    '弹窗保存后应把 saved.model 作为本次生成的模型'
  );
});

test('编辑器试听路径传的是草稿里选的模型', () => {
  // 页面把草稿值交给 hook（而不是已保存的 voiceModelName）。
  assert.match(
    editor,
    /useVoicePreview\(\{[\s\S]{0,300}?model:\s*voiceDraftModel[\s\S]{0,200}?\}\)/,
    '试听必须用草稿模型，而不是已保存的旧值'
  );
  assert.match(previewHook, /const request = \{ voice, ratePercent, model \};/, '请求体必须真的带模型');
  // 换模型后要丢弃上一条样音，否则界面显示"模型 B"却还在播模型 A 的声音。
  assert.match(
    previewHook,
    /useEffect\(\(\) => \{[\s\S]{0,400}?\}, \[model, voice, ratePercent, panelOpen\]\);/,
    '模型变化必须触发样音清理'
  );
});

test('模型不可用时的提示能指导用户改选', () => {
  // 后端回来的是 model_not_configured，前端若归到"试听失败"，
  // 用户会去查供应商，而真正该做的是重新选一个模型。
  assert.equal(m.classifyVoicePreviewError('model_not_configured'), 'model_missing');
  assert.match(
    previewHook,
    /case 'model_missing':[\s\S]{0,240}?voicePreviewModelMissing/,
    'model_missing 必须有专门提示，不能落到通用失败'
  );
});
