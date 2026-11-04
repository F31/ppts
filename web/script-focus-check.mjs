#!/usr/bin/env node
/**
 * script-focus-check —— 讲稿段落焦点（逐柱指代）的投影门禁。
 *
 * 守的是**保存时会不会误删已选焦点**：
 *   - 「不表态」的段落必须**没有** focus 键（写成 null 会被服务端当成清空）；
 *   - 「明确清空」必须发一个空目标，而不是省略字段；
 *   - 标签找不到对应候选时显示 id，不假装"未指定"。
 *
 * 这三条错了都不会报错：存稿成功、接口成功、日志干净，只是用户的焦点悄悄没了。
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const m = await import('./src/scriptFocus.ts');

const seg = (id, focus) => ({
  segmentId: id,
  slideId: 'slide-1',
  displayText: `文字-${id}`,
  spokenText: `文字-${id}`,
  status: 'draft',
  ...(focus ? { focus } : {})
});

test('不表态的段落保存时不带 focus 键', () => {
  const out = m.applyFocus([seg('seg-1'), seg('seg-2')], { 'seg-1': null });
  assert.equal('focus' in out[0], false, 'null 必须落成"没有这个字段"');
  assert.equal('focus' in out[1], false, '表里没提到的段也不能被写成 null');
  // 真正会被发出去的是 JSON：这里断言线上形态，而不是内存对象。
  const wire = JSON.parse(JSON.stringify(out));
  assert.equal('focus' in wire[0], false);
  assert.equal('focus' in wire[1], false);
});

test('明确清空发空目标，而不是省略字段', () => {
  const out = m.applyFocus([seg('seg-1', { shapeId: 'shape-3' })], { 'seg-1': m.EMPTY_FOCUS });
  assert.deepEqual(out[0].focus, { shapeId: '', barId: '' });
  const wire = JSON.parse(JSON.stringify(out));
  assert.deepEqual(wire[0].focus, { shapeId: '', barId: '' });
});

test('指向某根柱子时带上 barId', () => {
  const out = m.applyFocus([seg('seg-1')], { 'seg-1': { shapeId: 'shape-7', barId: 'p1-s2-b0' } });
  assert.deepEqual(out[0].focus, { shapeId: 'shape-7', barId: 'p1-s2-b0' });
});

test('文本字段不被焦点投影改动', () => {
  const base = seg('seg-1', { shapeId: 'shape-1' });
  const out = m.applyFocus([{ ...base, displayText: '改过的' }], { 'seg-1': null });
  assert.equal(out[0].displayText, '改过的');
  assert.equal(out[0].spokenText, base.spokenText);
});

test('initialFocusMap 把空目标读成不表态', () => {
  const map = m.initialFocusMap([
    seg('a', { shapeId: 'shape-1' }),
    seg('b', { shapeId: '', barId: '' }),
    seg('c')
  ]);
  assert.deepEqual(map.a, { shapeId: 'shape-1' });
  assert.equal(map.b, null, '空目标不该复活成一个"指向空"的值');
  assert.equal(map.c, null);
});

test('标签命中候选用候选的可读文案', () => {
  const targets = [
    { shapeId: 'shape-7', barId: 'p1-s2-b0', kind: 'bar', label: 'Q1 · 系列 A = 174', box: [0, 0, 1, 1] }
  ];
  assert.equal(m.focusLabelOf({ shapeId: 'shape-7', barId: 'p1-s2-b0' }, targets), 'Q1 · 系列 A = 174');
});

test('候选里没有这个目标时显示 id，不显示"未指定"', () => {
  assert.equal(m.focusLabelOf({ shapeId: 'shape-9', barId: 'b2' }, []), 'shape-9 · b2');
  assert.equal(m.focusLabelOf({ shapeId: 'shape-9' }, []), 'shape-9');
  assert.equal(m.focusLabelOf(null, []), '');
});

test('isSameFocus 区分同一图表的不同柱子', () => {
  assert.equal(m.isSameFocus({ shapeId: 's', barId: 'b1' }, { shapeId: 's', barId: 'b1' }), true);
  assert.equal(m.isSameFocus({ shapeId: 's', barId: 'b1' }, { shapeId: 's', barId: 'b2' }), false);
  assert.equal(m.isSameFocus({ shapeId: 's' }, { shapeId: 's', barId: '' }), true);
  assert.equal(m.isSameFocus(null, null), true);
});

test('编辑器真的接了这一套（不是另写一份）', async () => {
  const { readFileSync } = await import('node:fs');
  const src = readFileSync(new URL('./src/ScriptEditor.tsx', import.meta.url), 'utf8');
  assert.match(src, /from '\.\/scriptFocus'/, 'ScriptEditor 必须从 scriptFocus 引入投影');
  assert.match(src, /applyFocus\(/, '段落投影必须走 applyFocus');
  assert.doesNotMatch(src, /delete next\.focus/, '不能又留一份本地投影实现');
});
