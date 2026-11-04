// 插值语义矩阵：锁住「两端看到同一个数」这件事。
//
// 这些用例不看像素，只看 evaluateLayer 的返回值。它们存在的理由很具体：
// DOM 端（sharePlayer）与帧端（Remotion）最终都调用这一份代码，
// 但两端**消费方式**不同——帧端每帧调用，DOM 端在编译关键帧时调用。
// 只要返回值语义变了，两端会以不同方式错，而且错得不明显（比如 opacity 从相乘变成覆盖，
// 单条 clip 的页完全看不出来，只有叠加的页才暴露）。

import assert from 'node:assert/strict';
import test from 'node:test';

import { cubicBezier, evaluateLayer, FOCUS_DIM_OPACITY, focusMask, framesForDuration, IDENTITY } from './motion.ts';

const linear = { x1: 0, y1: 0, x2: 1, y2: 1 };

/** 做一个最小 layer：只关心 clips，其余字段取自 IR 的真实形状。 */
function layer(...clips) {
  return { kind: 'text', assetMode: 'bitmap-region', box: { x: 0, y: 0, w: 100, h: 40 }, clips };
}

function fadeIn(startMs, durationMs, from = 0, to = 1) {
  return {
    effect: 'fadeIn',
    startMs,
    durationMs,
    easing: linear,
    props: [{ name: 'opacity', from, to }]
  };
}

test('opacity 相乘：两个淡入叠在一起不会变得更不透明', () => {
  const state = evaluateLayer(layer(fadeIn(0, 1000, 0, 0.5), fadeIn(0, 1000, 0, 0.5)), 1000);
  // 0.5 × 0.5 = 0.25。若改成「后者覆盖」，这里会是 0.5——单条 clip 的页看不出来，
  // 只有叠加页暴露，所以必须显式断言。
  assert.equal(state.opacity, 0.25);
});

test('translate 相加：同方向叠加就是位移之和', () => {
  const a = {
    effect: 'slideIn', startMs: 0, durationMs: 1000, easing: linear,
    props: [{ name: 'translateY', from: 40, to: 0 }]
  };
  const b = {
    effect: 'slideIn', startMs: 0, durationMs: 1000, easing: linear,
    props: [{ name: 'translateY', from: 10, to: 0 }]
  };
  const state = evaluateLayer(layer(a, b), 500);
  // 各走一半：20 + 5 = 25
  assert.equal(state.ty, 25);
});

test('scale 相乘：0.9 × 1.1 回到 1', () => {
  const shrink = {
    effect: 'pop', startMs: 0, durationMs: 1000, easing: linear,
    props: [{ name: 'scale', from: 0.9, to: 1 }]
  };
  const grow = {
    effect: 'pop', startMs: 0, durationMs: 1000, easing: linear,
    props: [{ name: 'scale', from: 1, to: 1.1 }]
  };
  const state = evaluateLayer(layer(shrink, grow), 0);
  assert.ok(Math.abs(state.sx - 0.9) < 1e-9, `sx=${state.sx}`);
  assert.equal(state.sx, state.sy);
});

test('clip 顺序不影响结果（规格里书写顺序是用户自由，不该影响像素）', () => {
  const a = fadeIn(0, 1000, 0, 0.6);
  const b = {
    effect: 'slideIn', startMs: 0, durationMs: 1000, easing: linear,
    props: [{ name: 'translateX', from: 30, to: 0 }]
  };
  const forward = evaluateLayer(layer(a, b), 400);
  const backward = evaluateLayer(layer(b, a), 400);
  assert.deepEqual(forward, backward);
});

test('零长 clip：起点之后恒为 1（单页降级冻结一页的全部依据）', () => {
  const frozen = { effect: 'fadeIn', startMs: 0, durationMs: 0, easing: linear, props: [{ name: 'opacity', from: 0, to: 1 }] };
  assert.equal(evaluateLayer(layer(frozen), 0).opacity, 1);
  assert.equal(evaluateLayer(layer(frozen), 9999).opacity, 1);
  // 起点之前仍是 0：否则「零长 clip」会变成「从第 0 帧就全亮」，冻结语义就错了
  const later = { effect: 'fadeIn', startMs: 500, durationMs: 0, easing: linear, props: [{ name: 'opacity', from: 0, to: 1 }] };
  assert.equal(evaluateLayer(layer(later), 499).opacity, 0);
});

test('未知属性静默忽略：新增属性不该让旧合成器整片失败', () => {
  const unknown = {
    effect: 'blurIn', startMs: 0, durationMs: 1000, easing: linear,
    props: [{ name: 'blurRadius', from: 8, to: 0 }]
  };
  const state = evaluateLayer(layer(unknown), 500);
  assert.deepEqual(state, IDENTITY);
});

test('cubicBezier 端点与单调性', () => {
  const easeOut = { x1: 0.16, y1: 1, x2: 0.3, y2: 1 };
  assert.equal(cubicBezier(easeOut, 0), 0);
  assert.equal(cubicBezier(easeOut, 1), 1);
  let prev = -1;
  for (let i = 0; i <= 20; i++) {
    const v = cubicBezier(easeOut, i / 20);
    assert.ok(v >= prev - 1e-9, `在 x=${i / 20} 处不单调: ${v} < ${prev}`);
    prev = v;
  }
});

test('帧数口径与 Go 侧 round(durationMs × fps / 1000) 一致', () => {
  assert.equal(framesForDuration(6000, 30), 180);
  assert.equal(framesForDuration(16, 30), 1); // 0.48 → 0，但至少 1 帧
  assert.equal(framesForDuration(50, 30), 2); // 1.5 → 2（四舍五入，不是截断）
});

// 矩形焦点遮罩：内嵌图表的柱子没有图层，靠一块矩形指「此刻讲的是这根」。
// 这组用例守三件事：强度与图层压暗同源、放手必须归零、普通焦点不受影响。
test('focusMask：hold 期压到与图层压暗同强度', () => {
  const rect = { x: 385, y: 384, w: 173, h: 415 };
  const focus = [{ startMs: 1000, elementId: 'p1-s2-b0', rect, fadeMs: 240 }];
  assert.equal(focusMask(focus, 500), null, '段首之前不该有遮罩');
  assert.equal(focusMask(focus, 1000), null, '刚开始时 alpha=0，不画节点');
  const m = focusMask(focus, 2000);
  assert.ok(m);
  // 0.45：非焦点层此刻 opacity 是 FOCUS_DIM_OPACITY，遮罩要让画面同样淡下去。
  assert.equal(Math.round(m.alpha * 1000) / 1000, Math.round((1 - FOCUS_DIM_OPACITY) * 1000) / 1000);
  assert.deepEqual(m.rect, rect);
});

test('focusMask：放手后必须真的回到 0', () => {
  const rect = { x: 10, y: 10, w: 100, h: 100 };
  const focus = [{ startMs: 1000, elementId: 'b0', rect, endMs: 3000, fadeMs: 240 }];
  assert.ok(focusMask(focus, 3000), '放手那一刻还是满的');
  assert.equal(focusMask(focus, 3240), null, '淡出结束必须归零');
});

test('focusMask：没有 rect 的焦点不改变原有行为', () => {
  const focus = [{ startMs: 1000, elementId: 'e1' }];
  assert.equal(focusMask(focus, 2000), null);
  // 混排时也一样：最新一段是普通焦点，就不画遮罩。
  const mixed = [
    { startMs: 0, elementId: 'b0', rect: { x: 0, y: 0, w: 10, h: 10 } },
    { startMs: 1000, elementId: 'e1' }
  ];
  assert.equal(focusMask(mixed, 1500), null);
  assert.ok(focusMask(mixed, 500));
});
