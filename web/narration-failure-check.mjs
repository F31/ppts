#!/usr/bin/env node
/**
 * narration-failure-check —— 配音单页失败的可见性。
 *
 * 守的是：整单失败的 last_error 里要能拆出页、段和供应商原文；
 * 重试耗尽的包装不能把前缀吃掉；编辑器有文稿时用文稿页码，而不是任务里的序号。
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const m = await import('./src/narrationFailure.ts');

const t = (key, vars = {}) => {
  const table = {
    'editor.narrationPageFailed': '第 {page} 页配音失败：{reason}',
    'editor.narrationPageFailedTitle': '第 {page} 页（{title}）配音失败：{reason}'
  };
  let text = table[key] ?? key;
  for (const [name, value] of Object.entries(vars)) text = text.split(`{${name}}`).join(String(value));
  return text;
};

test('没有页前缀的错误原样留下', () => {
  assert.equal(m.parseNarrationPageFailure('connection reset by peer'), null);
  assert.equal(m.formatNarrationPageFailure('配音生成失败', [], t), '配音生成失败');
});

test('从重试耗尽包装里拆出页，并用文稿位置覆盖任务页序', () => {
  const raw =
    'job failed after 3 attempts: narration page failed: slideId=slide-b page=2 segment=seg-1: voice provider rejected the text';
  const parsed = m.parseNarrationPageFailure(raw);
  assert.deepEqual(parsed, {
    slideId: 'slide-b',
    page: 2,
    segmentId: 'seg-1',
    reason: 'voice provider rejected the text'
  });
  const slides = [
    { slideId: 'slide-a', title: '封面' },
    { slideId: 'slide-x', title: '' },
    { slideId: 'slide-b', title: '结论' }
  ];
  assert.equal(m.narrationPageNumber(parsed, slides), 3);
  assert.equal(m.formatNarrationPageFailure(raw, slides, t), '第 3 页（结论）配音失败：voice provider rejected the text');
});

test('没有标题时只显示页码，原因里的冒号保留', () => {
  const raw = 'narration page failed: slideId=slide-9 page=4 segment=seg-2: http 429: slow down';
  assert.equal(m.formatNarrationPageFailure(raw, [{ slideId: 'slide-9', title: '  ' }], t), '第 1 页配音失败：http 429: slow down');
});
