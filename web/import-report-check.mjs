#!/usr/bin/env node
/**
 * import-report-check —— 导入报告页纯逻辑门禁。
 *
 * 加载的就是页面真正调用的 src/importReport.ts，测的是"显示层最容易错一格"的几处：
 *   - 0 基页序必须换成 1 基（否则页面上会出现"第 0 页"）
 *   - 0 页时覆盖率必须是 0 而不是 NaN
 *   - 页数 0 不代表"不一致"（解析还没回写不等于回写错了）
 *   - 未知条目代码/未知逐页标记不能被吞掉
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const r = await import('./src/importReport.ts');

test('页序展示是 1 基，且去重升序', () => {
  assert.equal(r.humanizePages([0]).text, '1');
  assert.equal(r.humanizePages([2, 0, 2, 1]).text, '1、2、3');
  assert.equal(r.humanizePages([]).text, '');
  assert.equal(r.humanizePages(undefined).text, '');
});

test('页序超出上限时截断并报告 truncated', () => {
  const many = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9];
  const got = r.humanizePages(many, 4);
  assert.equal(got.text, '1、2、3、4');
  assert.equal(got.truncated, true);
  const exact = r.humanizePages([0, 1, 2, 3], 4);
  assert.equal(exact.truncated, false);
});

test('覆盖率：0 页是 0 不是 NaN，且夹在 0..100', () => {
  assert.equal(r.notesCoverage(0, 0), 0);
  assert.equal(r.notesCoverage(4, 3), 75);
  assert.equal(r.notesCoverage(3, 99), 100);
  assert.equal(r.notesCoverage(3, -1), 0);
  assert.ok(Number.isFinite(r.notesCoverage(0, 5)));
});

test('页数不一致判定：任一侧为 0 都不算不一致', () => {
  assert.equal(r.pageCountMismatch(3, 9), true);
  assert.equal(r.pageCountMismatch(3, 3), false);
  assert.equal(r.pageCountMismatch(0, 9), false);
  assert.equal(r.pageCountMismatch(3, 0), false);
});

test('条目排序：warn 在前，未知代码不丢且排在已知之后', () => {
  const input = [
    { code: 'missing_notes', severity: 'info', count: 5 },
    { code: 'future_thing', severity: 'info', count: 1 },
    { code: 'hidden_pages', severity: 'info', count: 2 },
    { code: 'opaque_shapes', severity: 'warn', count: 1 },
    { code: 'embedded_media', severity: 'warn', count: 3 }
  ];
  const ordered = r.orderFindings(input).map((f) => f.code);
  assert.deepEqual(ordered, [
    'opaque_shapes',
    'embedded_media',
    'hidden_pages',
    'missing_notes',
    'future_thing'
  ]);
  assert.equal(input[0].code, 'missing_notes', '排序不得改动入参');
});

test('错误归类覆盖服务端每个 code', () => {
  assert.equal(r.classifyReportError('report_not_ready'), 'not_ready');
  assert.equal(r.classifyReportError('revision_not_found'), 'revision_missing');
  assert.equal(r.classifyReportError('report_corrupt'), 'corrupt');
  assert.equal(r.classifyReportError('invalid_revision'), 'invalid');
  assert.equal(r.classifyReportError('http_403'), 'forbidden');
  assert.equal(r.classifyReportError(undefined), 'unknown');
  assert.equal(r.classifyReportError('something_else'), 'unknown');
});

test('逐页标记：opaque_shape 带出形状名，未知标记原样保留', () => {
  assert.deepEqual(r.describeFlag('hidden'), { kind: 'hidden', detail: '' });
  assert.deepEqual(r.describeFlag('opaque_shape:图表 3'), { kind: 'opaqueShape', detail: '图表 3' });
  assert.deepEqual(r.describeFlag('morph_transition'), { kind: 'unknown', detail: 'morph_transition' });
});
