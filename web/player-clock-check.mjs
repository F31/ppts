#!/usr/bin/env node
/**
 * player-clock-check —— 播放器时钟与字幕偏移的门禁。
 *
 * 覆盖三条同步链路共用的 playerClock.ts：
 *  1) spokenCharAt 的二分查找与朴素线性扫描逐点一致（P5 性能优化不改语义）；
 *  2) Chars 缺失时按行均分回退，与后端 media.SplitSubtitleCues 的口径对齐（P2）；
 *  3) syncOffset.ts 的成品固化值优先 / 草稿钳制（P1）。
 *
 * 用法：node --experimental-strip-types --test player-clock-check.mjs
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const clock = await import('./src/playerClock.ts');
const sync = await import('./src/syncOffset.ts');

// 朴素参考实现：取最后一个 startUs <= t 的字符下标。
function naiveSpoken(chars, t) {
  let idx = -1;
  for (let i = 0; i < chars.length; i++) {
    if (chars[i].startUs <= t) idx = i;
    else break;
  }
  return idx;
}

test('spokenCharAt binary search matches naive scan across all boundaries', () => {
  const chars = [
    { startUs: 0, endUs: 100, char: '你' },
    { startUs: 100, endUs: 200, char: '好' },
    { startUs: 200, endUs: 350, char: '世' },
    { startUs: 350, endUs: 500, char: '界' },
  ];
  const cue = { startUs: 0, endUs: 500, text: '你好世界', chars };
  for (let t = -50; t <= 550; t += 7) {
    assert.equal(clock.spokenCharAt(cue, t), naiveSpoken(chars, t), `t=${t}`);
  }
  for (const t of [0, 99, 100, 199, 200, 349, 350, 499, 500]) {
    assert.equal(clock.spokenCharAt(cue, t), naiveSpoken(chars, t), `boundary t=${t}`);
  }
});

test('spokenCharAt fallback distributes time by line, matching backend per-line split', () => {
  // 带显式换行的两行字幕，时间对半分。
  const text = '今天天气很好我们出去走走吧\n明天天气也不错我们在家休息吧';
  const cue = { startUs: 0, endUs: 2000, text, chars: undefined };
  const lines = clock.splitLines(cue);
  assert.equal(lines.length, 2, 'test cue should split into 2 lines');
  // 第一行中点落在第一行区间内；第二行中点落在第二行区间内。
  const firstMid = clock.spokenCharAt(cue, 500);
  const secondMid = clock.spokenCharAt(cue, 1500);
  assert.ok(firstMid >= lines[0].start && firstMid < lines[0].end, `first=${firstMid}`);
  assert.ok(secondMid >= lines[1].start && secondMid < lines[1].end, `second=${secondMid}`);
  // 单调性：后半段的高亮位置一定不早于前半段。
  assert.ok(secondMid > firstMid);
  // 边界：段前 -1，段后 = 文本长度。
  assert.equal(clock.spokenCharAt(cue, -1), -1);
  assert.equal(clock.spokenCharAt(cue, 2000), text.length);
});

test('effectiveOffsetMs prefers baked manifest value, clamps, then falls back to draft', () => {
  // manifest 固化值（含 0）优先，且被钳制到 ±2000。
  assert.equal(sync.effectiveOffsetMs(50, 'tk', 'pj'), 50);
  assert.equal(sync.effectiveOffsetMs(0, 'tk', 'pj'), 0);
  assert.equal(sync.effectiveOffsetMs(9999, 'tk', 'pj'), 2000);
  assert.equal(sync.effectiveOffsetMs(-9999, 'tk', 'pj'), -2000);
  // 无固化字段时读草稿：内存存储桩。
  const store = new Map();
  globalThis.window = { localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, v),
    removeItem: (k) => store.delete(k),
  } };
  try {
    sync.writeDraftOffset('tk', 'pj', 120);
    assert.equal(sync.effectiveOffsetMs(undefined, 'tk', 'pj'), 120);
    sync.clearDraftOffset('tk', 'pj');
    assert.equal(sync.effectiveOffsetMs(undefined, 'tk', 'pj'), 0);
    assert.equal(sync.readDraftOffset('tk', 'pj'), 0);
  } finally {
    delete globalThis.window;
  }
});
