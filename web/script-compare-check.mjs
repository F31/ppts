#!/usr/bin/env node
/**
 * script-compare-check —— 讲稿并排对照（G2-2）门禁。
 *
 * 直接加载 src/scriptCompare.ts（面板真正调用的那份），守四件事：
 *   - 切句不能把「23.5%」「e.g.」「“你好。”」切坏，否则会出现半句对照；
 *   - 对齐门槛：同义改写要判成 changed，完全无关的句子不能硬配对；
 *   - 「不改任何选择直接采用」必须等于当前讲稿（默认=接受建议稿，不偷偷改内容）；
 *   - 补回原文/删除新增句后顺序与内容都要对，且面板真的接进了编辑器。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/scriptCompare.ts');

const defaults = (rows) => Object.fromEntries(rows.map((row) => [row.key, m.defaultCompareChoice(row)]));

test('中文切句保留句末引号与重复终止符', () => {
  assert.deepEqual(m.splitSentences('他说：“你好。”然后离开了。真的吗？！'), [
    '他说：“你好。”',
    '然后离开了。',
    '真的吗？！'
  ]);
});

test('英文句点只在真句末切：小数与缩写不切', () => {
  assert.deepEqual(m.splitSentences('Revenue grew 23.5% this quarter. Growth is stable.'), [
    'Revenue grew 23.5% this quarter.',
    'Growth is stable.'
  ]);
  assert.deepEqual(m.splitSentences('We use e.g. two vendors. They agreed.'), [
    'We use e.g. two vendors.',
    'They agreed.'
  ]);
});

test('空文本与纯空白不产生句子', () => {
  assert.deepEqual(m.splitSentences(''), []);
  assert.deepEqual(m.splitSentences('  \n\n  '), []);
});

test('换行是硬边界且随句子保留：没有标点的分段不会被粘成一句', () => {
  assert.deepEqual(m.splitSentences('第一条\n第二条'), ['第一条\n', '第二条']);
});

test('多行讲稿"不改选择直接采用"必须逐字节还原（含换行）', () => {
  // 真实页面的原文常是"标题一行、要点一行"。若切句丢掉换行，采用后整页会被压成一段，
  // 这是比"个别句子判断错"更严重的内容损坏。
  const text = '智讲 PPT\n一次上传，多端讲解。\n客户方案讲解 ｜ 2026';
  const rows = m.buildCompareRows(text, text);
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), text);
});

test('展示文本去掉句尾换行，但合并结果保留', () => {
  assert.equal(m.displaySentence('要点一\n'), '要点一');
  assert.equal(m.displaySentence('要点一'), '要点一');
  assert.equal(m.displaySentence(undefined), '');
  const rows = m.buildCompareRows('标题\n要点一', '标题\n要点一');
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), '标题\n要点一');
});

test('完全相同的文本判 same，且默认合并结果不变', () => {
  const text = '本季度营收增长 23%。毛利率提升 2 个百分点。';
  const rows = m.buildCompareRows(text, text);
  assert.equal(rows.length, 2);
  assert.ok(rows.every((row) => row.kind === 'same'));
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), text);
});

test('同义改写判 changed，默认采用润色稿', () => {
  const original = '本季度营收同比增长 23%。';
  const current = '这一季度我们的营收同比增长了百分之二十三。';
  const rows = m.buildCompareRows(original, current);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].kind, 'changed');
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), current);
});

test('新增句判 added，默认保留；选择删除后消失', () => {
  const original = '营收增长 23%。';
  const current = '营收增长 23%。这里补充一句背景。';
  const rows = m.buildCompareRows(original, current);
  const added = rows.find((row) => row.kind === 'added');
  assert.ok(added, '第二句应判为新增');
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), current);

  const choices = { ...defaults(rows), [added.key]: 'original' };
  assert.equal(m.mergeCompareRows(rows, choices), '营收增长 23%。');
});

test('被模型删掉的句子判 removed，补回时回到原来的位置', () => {
  const original = '第一句。第二句要保留。第三句。';
  const current = '第一句。第三句。';
  const rows = m.buildCompareRows(original, current);
  const removed = rows.find((row) => row.kind === 'removed');
  assert.ok(removed, '中间那句应判为删除');
  assert.equal(rows.indexOf(removed), 1, '删除行必须落在它原来的位置');

  const choices = { ...defaults(rows), [removed.key]: 'original' };
  // 补回后顺序必须是 1→2→3，而不是被追加到末尾——这正是"部分接受"最容易做错的地方。
  const merged = m.mergeCompareRows(rows, choices);
  assert.equal(merged, original);
});

test('无关句子不能硬配对：判成一删一增，而不是 changed', () => {
  const rows = m.buildCompareRows('今天天气不错。', '服务器需要扩容。');
  const kinds = rows.map((row) => row.kind).sort();
  assert.deepEqual(kinds, ['added', 'removed']);
});

test('原文为空时全部是新增，合并等于讲稿', () => {
  const current = '第一句。第二句。';
  const rows = m.buildCompareRows('', current);
  assert.ok(rows.length === 2 && rows.every((row) => row.kind === 'added'));
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), current);
});

test('讲稿为空时全部是删除，默认采用结果是空串', () => {
  const rows = m.buildCompareRows('第一句。第二句。', '');
  assert.ok(rows.length === 2 && rows.every((row) => row.kind === 'removed'));
  assert.equal(m.mergeCompareRows(rows, defaults(rows)), '');
  const all = Object.fromEntries(rows.map((row) => [row.key, 'original']));
  assert.equal(m.mergeCompareRows(rows, all), '第一句。第二句。');
});

test('中英混排拼回时的空格处理', () => {
  assert.equal(m.joinSentences(['Growth is stable.', 'Next quarter.']), 'Growth is stable. Next quarter.');
  assert.equal(m.joinSentences(['增长稳定。', '下个季度再看。']), '增长稳定。下个季度再看。');
});

test('汇总数字反映"这次会改什么"', () => {
  const rows = m.buildCompareRows('第一句。第二句要保留。', '第一句话变了。第三句是新增。');
  const choices = defaults(rows);
  const summary = m.summarizeCompare(rows, choices);
  assert.equal(summary.total, rows.length);
  assert.equal(summary.reverted, 0);
  assert.equal(summary.dropped, 0);
  for (const key of Object.keys(choices)) {
    const row = rows.find((item) => item.key === key);
    if (row.kind === 'removed') choices[key] = 'original';
    if (row.kind === 'added') choices[key] = 'original';
  }
  const after = m.summarizeCompare(rows, choices);
  assert.equal(after.reverted, 1);
  assert.equal(after.dropped, 1);
});

test('面板真的接进了编辑器与页面（不是只写了模块）', () => {
  const editor = readFileSync(new URL('./src/ScriptEditor.tsx', import.meta.url), 'utf8');
  assert.match(editor, /onCompareScript/, '编辑器要给出「与原稿对照」入口');
  assert.match(editor, /applyText/, 'handle 必须能把合并结果写回段落（走既有自动保存）');

  const page = readFileSync(new URL('./src/pages/ProjectEditor.tsx', import.meta.url), 'utf8');
  assert.match(page, /getScriptCompare\(/, '页面必须调用对照端点');
  assert.match(page, /<ScriptCompareDialog/, '对照面板必须真的渲染');

  const panel = readFileSync(new URL('./src/components/ScriptCompareDialog.tsx', import.meta.url), 'utf8');
  assert.match(panel, /mergeCompareRows\(/, '采用按钮必须走 mergeCompareRows');
});
