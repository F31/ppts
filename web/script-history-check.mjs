#!/usr/bin/env node
/**
 * script-history-check —— 讲稿撤销/重做的纯逻辑门禁。
 *
 * 与 animation-check / core-check 同一风格：直接加载 src/scriptHistory.ts，
 * 测的就是组件真正调用的那份代码，不存在"测试抄一份逻辑"。
 *
 * 重点不在"能撤销"，而在几条容易写错、写错了用户会立刻感觉到的地方：
 *   - 连续打字要合并成一步（否则撤销退化成一次删一个字）
 *   - 停手超过窗口要分成两步（否则一句话写完只能整段撤销）
 *   - 撤销后再输入要清掉重做链
 *   - 快照必须与 state 隔离（存引用会让"历史"跟着当前文本一起变）
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const h = await import('./src/scriptHistory.ts');
const { SCRIPT_COALESCE_MS, SCRIPT_HISTORY_LIMIT } = h;

const snap = (text, spoken = text) => ({
  texts: { a: text, b: '' },
  spokenTexts: { a: spoken, b: '' }
});

test('空历史：不能撤销也不能重做', () => {
  const empty = h.emptyHistory();
  assert.equal(h.canUndo(empty), false);
  assert.equal(h.canRedo(empty), false);
  assert.equal(h.undo(empty, snap('x')), null);
  assert.equal(h.redo(empty, snap('x')), null);
});

test('一次编辑后撤销回到编辑前，再重做回到编辑后', () => {
  const t0 = 1000;
  let history = h.recordEdit(h.emptyHistory(), snap('原句'), 'a', t0);
  assert.equal(h.canUndo(history), true);

  const undone = h.undo(history, snap('改过的'));
  assert.ok(undone);
  assert.equal(undone.snapshot.texts.a, '原句');
  history = undone.history;
  assert.equal(h.canRedo(history), true);

  const redone = h.redo(history, snap('原句'));
  assert.ok(redone);
  assert.equal(redone.snapshot.texts.a, '改过的');
});

test('同段落连续输入在窗口内合并为一步', () => {
  let history = h.emptyHistory();
  // 模拟逐字输入：每次都以"编辑前"的快照记录
  history = h.recordEdit(history, snap(''), 'a', 1000);
  history = h.recordEdit(history, snap('第'), 'a', 1100);
  history = h.recordEdit(history, snap('第一'), 'a', 1200);
  history = h.recordEdit(history, snap('第一句'), 'a', 1300);
  assert.equal(history.past.length, 1, '连续输入应只留一步');
  // 这一步撤回去是输入开始前的内容
  const undone = h.undo(history, snap('第一句'));
  assert.equal(undone.snapshot.texts.a, '');
});

test('停手超过窗口记为另一步', () => {
  let history = h.recordEdit(h.emptyHistory(), snap(''), 'a', 1000);
  history = h.recordEdit(history, snap('第一句'), 'a', 1000 + SCRIPT_COALESCE_MS + 1);
  assert.equal(history.past.length, 2);
  // 连撤两次才回到最初
  let current = snap('第二句');
  const u1 = h.undo(history, current);
  assert.equal(u1.snapshot.texts.a, '第一句');
  const u2 = h.undo(u1.history, u1.snapshot);
  assert.equal(u2.snapshot.texts.a, '');
});

test('换段落立刻新开一步，即使间隔很短', () => {
  let history = h.recordEdit(h.emptyHistory(), snap(''), 'a', 1000);
  history = h.recordEdit(history, snap('a 改'), 'b', 1050);
  assert.equal(history.past.length, 2, '换段落后不能与上一段合并');
});

test('撤销后再输入会清掉重做链', () => {
  let history = h.recordEdit(h.emptyHistory(), snap('原句'), 'a', 1000);
  const undone = h.undo(history, snap('改过'));
  history = undone.history;
  assert.equal(h.canRedo(history), true);
  // 撤销之后接着打字：这正是"撤回去又改了别的"，旧的重做目标已经无意义
  history = h.recordEdit(history, snap('另一个方向'), 'a', 2000);
  assert.equal(h.canRedo(history), false);
});

test('快照与 state 隔离：记录后继续改原对象不影响历史', () => {
  const live = snap('原句');
  let history = h.recordEdit(h.emptyHistory(), live, 'a', 1000);
  live.texts.a = '被就地改掉';
  const undone = h.undo(history, snap('当前'));
  assert.equal(undone.snapshot.texts.a, '原句');
});

test('历史长度有上限，超出时丢最旧的', () => {
  let history = h.emptyHistory();
  for (let i = 0; i < SCRIPT_HISTORY_LIMIT + 10; i++) {
    // 每次换 key 并推进时间，确保每步都真的入栈
    history = h.recordEdit(history, snap(`第 ${i} 版`), `seg-${i}`, 1000 + i * 1000);
  }
  assert.equal(history.past.length, SCRIPT_HISTORY_LIMIT);
  // 最旧的一步应该是第 (i - LIMIT) 版之前的内容，而不是第 0 版
  assert.equal(history.past[0].texts.a, `第 10 版`);
});
