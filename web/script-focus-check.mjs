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

// ---- 焦点规则建议：只读 + 人工确认 ----
//
// 这一组守的不是「有没有返回数据」，而是三条会被静默违反的产品口径：
//   · 建议只能落在候选清单里（否则成片指不到那个形状）；
//   · 只自动采纳唯一候选，多候选与已人工指过的段一律不动；
//   · 采纳后仍走 applyFocus 三态：没采纳的段在 wire 上不能冒出 "focus": null。
// 三者错了都不报错：接口 200、保存成功，只是用户的焦点被机器悄悄改掉。

const suggestTargets = [
  { shapeId: 's1', barId: '', kind: 'text', label: '标题', box: [0, 0, 1, 1] },
  { shapeId: 's2', barId: '', kind: 'text', label: '第一条', box: [0, 0, 1, 1] },
  // 图表切开后候选里只有柱子，没有「整块图表」；建议若给 chart-1 的整形状必须被拦下。
  { shapeId: 'chart-1', barId: 'p1-s2-b0', kind: 'bar', label: 'Q1 · 174', box: [0, 0, 1, 1] }
];

const suggestion = (segmentId, candidates, reason = '') => ({
  segmentId,
  candidates: candidates.map(([shapeId, source]) => ({ shapeId, source })),
  reason
});

test('suggestedOptions 只保留候选清单里真有的目标', () => {
  const suggestions = [
    suggestion('a', [['s1', 'literal'], ['ghost', 'literal'], ['chart-1', 'literal']])
  ];
  const options = m.suggestedOptions(suggestions, 'a', suggestTargets);
  assert.equal(options.length, 1, '装饰/不存在的目标、以及整块图表都不能进建议');
  assert.deepEqual(options[0].focus, { shapeId: 's1', barId: '' });
  assert.equal(options[0].label, '标题', '建议要用候选清单的可读文案，不暴露裸 id');
  assert.equal(options[0].source, 'literal', '来源要透出来供界面标注「规则建议」');
  assert.deepEqual(m.suggestedOptions(suggestions, 'nope', suggestTargets), []);
  assert.deepEqual(m.suggestedOptions(null, 'a', suggestTargets), []);
});

test('acceptUniqueSuggestions 只采纳唯一候选，人工焦点优先', () => {
  const segments = [seg('a'), seg('b'), seg('c', { shapeId: 's1' }), seg('d'), seg('e')];
  const focus = m.initialFocusMap(segments);
  const suggestions = [
    suggestion('a', [['s1', 'literal']]),
    suggestion('b', [['s1', 'literal'], ['s2', 'keyword']]),
    suggestion('c', [['s2', 'literal']]),
    suggestion('d', []),
    // 规则给了 2 个、其中 1 个不可选：过滤后只剩 1 个也不能自动采纳。
    // 规则本身有歧义时该由人挑，这一条把「多候选闸门」和「候选清单闸门」分开。
    suggestion('e', [['s2', 'literal'], ['ghost', 'literal']])
  ];
  const { focus: next, accepted } = m.acceptUniqueSuggestions(segments, focus, suggestions, suggestTargets);
  assert.ok(accepted.length > 0, '夹具必须真的产生被采纳的建议，否则这组判据在主场沉默');
  assert.deepEqual(accepted, ['a'], '多候选 / 已指过 / 没建议的段都不能被采纳');
  assert.deepEqual(next.a, { shapeId: 's1', barId: '' });
  assert.equal(next.b, null);
  assert.deepEqual(next.c, { shapeId: 's1' }, '人工已选的焦点不能被规则覆盖');
  assert.equal(next.d, null, '没建议的段必须保持 null，不能变成 undefined');
  assert.equal(next.e, null, '候选清单过滤后只剩一个，也不能把「规则本来有歧义」的段自动采纳');
  // 纯函数：调用方的表不能被就地改写。
  assert.equal(focus.a, null);
  assert.notStrictEqual(next, focus);
});

test('批量采纳与逐段菜单共用候选清单闸门', () => {
  // 后端已经过滤过，但脏数据或版本串页仍可能给出成片指不到的目标；
  // 批量路径一旦绕过 suggestedOptions，用户点一下就会写进一个死焦点。
  const segments = [seg('a'), seg('b')];
  const dirty = [
    suggestion('a', [['ghost', 'literal']]),
    suggestion('b', [['chart-1', 'literal']]) // chart-1 只有柱子候选，没有整形状
  ];
  assert.deepEqual(m.suggestedOptions(dirty, 'a', suggestTargets), []);
  assert.deepEqual(m.suggestedOptions(dirty, 'b', suggestTargets), []);
  const blocked = m.acceptUniqueSuggestions(segments, m.initialFocusMap(segments), dirty, suggestTargets);
  assert.deepEqual(blocked.accepted, [], '两个「唯一候选」都不可选，一个都不能采纳');
  assert.equal(blocked.focus.a, null);
  assert.equal(blocked.focus.b, null);

  // 反例守沉默：合法候选不能被闸门一起拦掉，否则这条判据恒真。
  const mixed = [suggestion('a', [['ghost', 'literal']]), suggestion('b', [['s1', 'literal']])];
  const ok = m.acceptUniqueSuggestions(segments, m.initialFocusMap(segments), mixed, suggestTargets);
  assert.deepEqual(ok.accepted, ['b']);
  assert.equal(ok.focus.a, null);
  assert.deepEqual(ok.focus.b, { shapeId: 's1', barId: '' });
});

test('采纳后仍走三态：未采纳段在 wire 上不带 focus 键', () => {
  const segments = [seg('a'), seg('b'), seg('c', { shapeId: 's1' }), seg('d')];
  const { focus: next } = m.acceptUniqueSuggestions(
    segments,
    m.initialFocusMap(segments),
    [suggestion('a', [['s1', 'literal']]), suggestion('b', [['s1', 'literal'], ['s2', 'literal']])],
    suggestTargets
  );
  const wire = JSON.parse(JSON.stringify(m.applyFocus(segments, next)));
  assert.deepEqual(wire[0].focus, { shapeId: 's1', barId: '' });
  assert.equal('focus' in wire[1], false, '多候选未采纳仍是「不表态」，不能被当成清空');
  assert.equal('focus' in wire[3], false);
  assert.doesNotMatch(JSON.stringify(wire), /"focus":null/, '"focus": null 会被服务端按清空处理');
});

test('没有建议时原样返回，不制造一次写', () => {
  const segments = [seg('a')];
  const focus = m.initialFocusMap(segments);
  for (const empty of [null, undefined, []]) {
    const result = m.acceptUniqueSuggestions(segments, focus, empty, suggestTargets);
    assert.deepEqual(result.accepted, []);
    assert.strictEqual(result.focus, focus, '没建议时连新对象都不该造，避免触发一次无意义的保存');
  }
});

test('建议是只读的：没有采纳写端点，界面也不自己发请求', async () => {
  const { readFileSync } = await import('node:fs');
  const editor = readFileSync(new URL('./src/ScriptEditor.tsx', import.meta.url), 'utf8');
  const page = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
  const api = readFileSync(new URL('./src/api/scripts.ts', import.meta.url), 'utf8');
  const go = readFileSync(new URL('../internal/api/editor.go', import.meta.url), 'utf8');

  assert.match(go, /mux\.Handle\("GET \/projects\/\{pid\}\/slides\/\{sid\}\/focus-suggestions"/, '后端必须注册只读 GET');
  assert.doesNotMatch(go, /mux\.Handle\("(?:POST|PUT|PATCH|DELETE)[^"]*focus-suggestions"/, '不能新增「采纳」写端点');
  // 按函数体切片判定：路径字面量与 getJSON( 各占一行，用 [^\n]* 串起来会永远绿。
  const suggestFn = api.slice(api.indexOf('export async function getFocusSuggestions'), api.indexOf('export async function updateScript'));
  assert.match(suggestFn, /return getJSON<FocusSuggestions>\(/, '建议只能通过 GET 取数');
  assert.doesNotMatch(suggestFn, /\b(?:postJSON|putJSON|patchJSON|deleteJSON|connectJSON)\s*\(/, '前端不能给建议配写请求');

  assert.match(page, /getFocusSuggestions\(/, '页面要真的调用只读端点');
  assert.equal((page.match(/getFocusSuggestions\(/g) || []).length, 1, '只允许一处调用，防止有人在 effect 里又自动拉一次');
  assert.match(page, /onNeedFocusSuggestions=/, '必须由用户动作触发，不能自动预填');
  assert.doesNotMatch(editor, /\bfetch\s*\(|getJSON\s*\(|['"`]\/projects\//, '组件不直接发请求，取数只在 api 层');
  assert.match(editor, /acceptUniqueSuggestions\(/, '批量采纳要经纯函数判据');
  assert.match(editor, /suggestedOptions\(/, '逐段建议要过候选清单闸门');
  assert.match(editor, /const applyFocusMap =/, '焦点写入必须有唯一入口');
  assert.match(editor, /applyFocusMap\(next\)/, '批量采纳必须走与手选相同的保存通道');
  assert.match(editor, /setSegmentFocus\(segment\.segmentId, option\.focus\)/, '逐段点选建议也要走既有通道');
});
