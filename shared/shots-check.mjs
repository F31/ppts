// 页内镜头的求值矩阵。
//
// 这些用例不看像素，只看「某一毫秒该显示哪一镜、叠化到什么程度」。它们存在的
// 理由和 motion-check 一样：两端的消费方式不同（DOM 每帧求值、合成器每帧渲染），
// 只要区间端点差一帧，同一时刻就会给出两张不同的图——而那在单端的截图里
// 看不出来，只在两端并排比时露出来。

import assert from 'node:assert/strict';
import test from 'node:test';

import { shotAt } from './shots.ts';

function shot(index, startMs, durationMs, crossFadeMs = 0, image = `shot-${index}.png`) {
  return { index, image, startMs, durationMs, crossFadeMs };
}

/** 一页两镜：0–2000ms 全图，2000–4000ms 特写，切点带 120ms 叠化。 */
const two = [shot(0, 0, 2000), shot(1, 2000, 2000, 120, 'close.png')];

test('没有镜头时返回 null（调用方退回页级 image）', () => {
  assert.equal(shotAt(undefined, 0), null);
  assert.equal(shotAt([], 1000), null);
});

test('切点之前仍是第一镜，且不是叠化态', () => {
  const f = shotAt(two, 1999);
  assert.equal(f.index, 0);
  assert.equal(f.from, undefined);
  assert.equal(f.progress, 1);
});

test('切点当刻进入叠化：新镜 progress 从 0 起，旧镜仍在场', () => {
  const f = shotAt(two, 2000);
  assert.equal(f.index, 1);
  assert.equal(f.shot.image, 'close.png');
  assert.equal(f.from?.image, 'shot-0.png');
  assert.equal(f.progress, 0);
});

test('叠化中段在 0 与 1 之间，且随时间是单调的', () => {
  const a = shotAt(two, 2030).progress;
  const b = shotAt(two, 2060).progress;
  assert.ok(a > 0 && a < 1, `2030ms 的进度应在开区间内，实得 ${a}`);
  assert.ok(b > a && b < 1, `2060ms 应继续推进，实得 ${a} → ${b}`);
});

test('叠化窗口是左闭右开：窗口右端必须已经是纯新镜', () => {
  // 差 1ms 还在叠化，正好到点就是 1。写成闭区间会让"叠化结束"多停一帧，
  // 33ms 的混合帧在门禁里就是一条与哪张参照物都对不上的样本。
  assert.ok(shotAt(two, 2119).progress < 1);
  const done = shotAt(two, 2120);
  assert.equal(done.progress, 1);
  assert.equal(done.from, undefined);
  assert.equal(done.shot.image, 'close.png');
});

test('叠化结束后不再报告 from（否则调用方会多画一张图）', () => {
  const f = shotAt(two, 3000);
  assert.equal(f.index, 1);
  assert.equal(f.from, undefined);
  assert.equal(f.progress, 1);
});

test('crossFadeMs 为 0 是硬切：切点当刻就是新镜', () => {
  const hard = [shot(0, 0, 1000), shot(1, 1000, 1000, 0, 'b.png')];
  const f = shotAt(hard, 1000);
  assert.equal(f.index, 1);
  assert.equal(f.from, undefined);
  assert.equal(f.progress, 1);
});

test('超出末镜时停在末镜（页尾多取几毫秒不该回到第一镜）', () => {
  const f = shotAt(two, 9999);
  assert.equal(f.index, 1);
  assert.equal(f.shot.image, 'close.png');
});

test('负时间不越界：落到第一镜', () => {
  const f = shotAt(two, -5);
  assert.equal(f.index, 0);
  assert.equal(f.shot.image, 'shot-0.png');
});

test('三镜时每一段都能被选中（不是只认前两镜）', () => {
  const three = [shot(0, 0, 1000), shot(1, 1000, 1000, 120, 'b.png'), shot(2, 2000, 1000, 120, 'c.png')];
  assert.equal(shotAt(three, 500).shot.image, 'shot-0.png');
  assert.equal(shotAt(three, 1500).shot.image, 'b.png');
  assert.equal(shotAt(three, 2500).shot.image, 'c.png');
  // 第二处切换同样从上一镜叠过来（不是每次都从第一镜）
  assert.equal(shotAt(three, 2000).from?.image, 'b.png');
});
