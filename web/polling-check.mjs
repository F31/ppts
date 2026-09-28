import { test } from 'node:test';
import assert from 'node:assert/strict';
import { pollingBackoffMs } from './src/hooks/polling.ts';

test('无失败时回归基础间隔', () => {
  assert.equal(pollingBackoffMs(2000, 0, 30000), 2000);
  assert.equal(pollingBackoffMs(5000, -1, 30000), 5000);
});

test('失败后按失败次数指数退避', () => {
  assert.equal(pollingBackoffMs(2000, 1, 30000), 2000);
  assert.equal(pollingBackoffMs(2000, 2, 30000), 4000);
  assert.equal(pollingBackoffMs(2000, 3, 30000), 8000);
  assert.equal(pollingBackoffMs(2000, 4, 30000), 16000);
  assert.equal(pollingBackoffMs(5000, 3, 30000), 20000);
});

test('退避封顶在 maxBackoffMs', () => {
  assert.equal(pollingBackoffMs(2000, 10, 30000), 30000);
  assert.equal(pollingBackoffMs(5000, 100, 30000), 30000);
});

// 反向验证：若有人把实现改回「失败也照打原节奏」（return base），上面的增长/封顶断言会翻红。
test('旧实现（永远返回 base）必然不等于退避结果——反向验证门禁有效', () => {
  const legacy = (base) => base; // 旧行为：无视失败次数
  assert.notEqual(legacy(2000), pollingBackoffMs(2000, 3, 30000));
});
