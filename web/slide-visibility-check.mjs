#!/usr/bin/env node
/**
 * slide-visibility-check —— 隐藏页作用域过滤的纯逻辑门禁。
 *
 * 这里要守住的是「默认跳过隐藏页」这件事本身：它决定用户点"全部重新生成"时
 * 给哪些页花钱。写错的表现不是报错，而是**多配了几页不会播出的音**，界面上看不出来。
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const v = await import('./src/slideVisibility.ts');
const { HIDDEN_PAGE_FLAG, isHiddenPage, scopeSlideIds, hiddenCount } = v;

const slide = (id, hidden = false) => ({
  slideId: id,
  featureFlags: hidden ? [HIDDEN_PAGE_FLAG] : []
});
const idOf = (s) => s.slideId;
const slides = [slide('s1'), slide('s2', true), slide('s3'), slide('s4', true)];

test('认 hidden 标记；没有标记、标记为空、缺字段都算可见', () => {
  assert.equal(isHiddenPage(slide('s1', true)), true);
  assert.equal(isHiddenPage(slide('s1')), false);
  assert.equal(isHiddenPage({ featureFlags: ['picture'] }), false);
  assert.equal(isHiddenPage({}), false);
  assert.equal(isHiddenPage(null), false);
});

test('默认排除隐藏页，且保持原顺序', () => {
  const ids = ['s1', 's2', 's3', 's4'];
  assert.deepEqual(scopeSlideIds(slides, ids, idOf, false), ['s1', 's3']);
});

test('显式纳入时原样返回', () => {
  const ids = ['s1', 's2', 's3', 's4'];
  assert.deepEqual(scopeSlideIds(slides, ids, idOf, true), ids);
  // 顺序必须与传入一致，不能因为集合运算被打乱（配音顺序影响时间轴）
  assert.deepEqual(scopeSlideIds(slides, ['s4', 's2'], idOf, true), ['s4', 's2']);
});

test('列表里查不到的页按可见处理，不静默丢页', () => {
  // 幻灯片列表尚未加载完时，ids 里可能有列表里没有的页；丢页的后果是"少配了一页"，
  // 比多配一页更难发现。
  const partial = [slide('s1')];
  assert.deepEqual(scopeSlideIds(partial, ['s1', 's9'], idOf, false), ['s1', 's9']);
});

test('空作用域返回空，不抛', () => {
  assert.deepEqual(scopeSlideIds(slides, [], idOf, false), []);
  assert.deepEqual(scopeSlideIds([], ['s1'], idOf, false), ['s1']);
});

test('隐藏页计数供界面提示使用', () => {
  assert.equal(hiddenCount(slides), 2);
  assert.equal(hiddenCount([slide('s1')]), 0);
  assert.equal(hiddenCount([]), 0);
});
