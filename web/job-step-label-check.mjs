#!/usr/bin/env node
/**
 * job-step-label-check —— 任务步骤必须能看出是哪一页。
 *
 * 有标题就写「第 N 页 · 标题」，分段配音再带上第几段。
 * 没有标题时只写页码，不能退回 slide-285 这种内部 id。
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const m = await import('./src/jobStepLabel.ts');

const t = (key, vars = {}) => {
  const table = {
    'jobs.stepPage': '第 {page} 页',
    'jobs.stepPageTitle': '第 {page} 页 · {title}',
    'jobs.stepPageSegment': '第 {page} 页 · 第 {segment} 段',
    'jobs.stepPageTitleSegment': '第 {page} 页 · {title} · 第 {segment} 段'
  };
  let text = table[key] ?? key;
  for (const [name, value] of Object.entries(vars)) text = text.split(`{${name}}`).join(String(value));
  return text;
};

test('有标题的分段写出页码、标题和段号', () => {
  assert.equal(
    m.stepSubjectHeading({ page: 30, title: '结论', segment: 2, summary: '余额不足' }, t),
    '第 30 页 · 结论 · 第 2 段'
  );
});

test('没有标题时只写页码，受影响页不再露出内部 id', () => {
  assert.equal(m.stepSubjectHeading({ page: 4 }, t), '第 4 页');
  assert.equal(
    m.affectedPageText(
      ['slide-a', 'slide-b'],
      [
        { slideId: 'slide-a', page: 1, title: '封面' },
        { slideId: 'slide-b', page: 30 }
      ],
      t
    ),
    '第 1 页 · 封面、第 30 页'
  );
});

test('对不上标签时保留原 id，时间轴步骤没有页标题', () => {
  assert.equal(m.stepSubjectHeading({}, t), '');
  assert.equal(m.affectedPageText(['slide-9'], [], t), 'slide-9');
});
