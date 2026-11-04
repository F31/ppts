// 插值语义矩阵：锁住「两端看到同一个数」这件事。
//
// 这些用例不看像素，只看 evaluateLayer 的返回值。它们存在的理由很具体：
// DOM 端（sharePlayer）与帧端（Remotion）最终都调用这一份代码，
// 但两端**消费方式**不同——帧端每帧调用，DOM 端在编译关键帧时调用。
// 只要返回值语义变了，两端会以不同方式错，而且错得不明显（比如 opacity 从相乘变成覆盖，
// 单条 clip 的页完全看不出来，只有叠加的页才暴露）。

import assert from 'node:assert/strict';
import test from 'node:test';

import { cubicBezier, evaluateLayer, FOCUS_DIM_OPACITY, focusDim, focusMask, framesForDuration, IDENTITY } from './motion.ts';

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

test('focusMask：endMs 写成 0 与「缺席」同义，不是「这一刻就放手」', () => {
  // IR 里 endMs 是 omitempty，0 的含义是「保持到下一段」。从前这里用
  // `!== undefined` 判，一份把 0 显式写出来的计划就会 tMs >= 0 恒成立 → alpha 0
  // → 遮罩整段消失，而同一页的图层焦点照常压暗（focusDim 用的是真值判）。
  // 症状只有一条：这一页跟完全没有焦点时一模一样。
  const rect = { x: 10, y: 10, w: 100, h: 100 };
  const zero = [{ startMs: 1000, elementId: 'b0', rect, endMs: 0, fadeMs: 240 }];
  const m = focusMask(zero, 2000);
  assert.ok(m, 'endMs=0 时遮罩必须还在');
  assert.equal(Math.round(m.alpha * 1000) / 1000, Math.round((1 - FOCUS_DIM_OPACITY) * 1000) / 1000);
  assert.equal(focusMask(zero, 1000), null, '刚开始那一下仍然是 alpha=0');
  // 缺字段与写 0 必须完全等价：把两者一起比，才不会被“两边都错了”蒙过去。
  const absent = [{ startMs: 1000, elementId: 'b0', rect, fadeMs: 240 }];
  for (const t of [0, 1000, 1240, 2000, 5999]) {
    assert.deepEqual(focusMask(zero, t), focusMask(absent, t), `t=${t} 时两种写法应当等价`);
  }
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

// 矩形焦点与图层压暗是**两套**让位机制，不能同时作用在同一块画面上。
// 混用时的像素形态是：孔里露出来的柱子被图层压过一道（0.55），再叠 45% 白纱——
// 「指谁谁灰」。这组用例守的正是这条边界。
test('focusDim：矩形焦点期间图层不再压暗', () => {
  const rect = { x: 385, y: 384, w: 173, h: 415 };
  const focus = [
    { startMs: 0, elementId: 'p1-s2', fadeMs: 240 },
    { startMs: 1000, elementId: 'p1-s2-b0', rect, fadeMs: 240 }
  ];
  // 第 0 段讲整张图：图是满的，柱子（不是图层）在表里记成退后，但没有层可查。
  assert.equal(focusDim(focus, 500)['p1-s2'], 0);
  // 第 1 段讲某根柱：让位交给遮罩，图表层必须回到全亮。
  const d = focusDim(focus, 2000);
  assert.equal(d['p1-s2'], undefined, '矩形焦点期间不应再压暗同页图表层');
  // 切换处要有淡出尾巴：不能一帧从 0.55 跳回 1.00。
  const mid = focusDim(focus, 1060);
  assert.ok(mid['p1-s2'] > 0 && mid['p1-s2'] < 1,
    `切换中该在 0~1 之间，实得 ${mid['p1-s2']}`);
  // 纯矩形焦点的页（前面没有图层焦点）：一律不压暗。
  assert.deepEqual(focusDim([{ startMs: 0, elementId: 'b0', rect, fadeMs: 240 }], 500), {});
});

// 每段矩形的**归属**：播放器是「一段焦点一个遮罩节点」，每个节点必须知道自己
// 此刻该不该亮。归属不能用矩形本身来判——两段的原点完全可以相同（先讲整张卡片、
// 再回来讲卡片上的标题块，这是最常见的编法），也不能用 elementId（一页里回到
// 同一个元素讲第二次同样常见）。`index` 是唯一没有歧义的答案，这条把它钉住。
test('focusMask：返回当前生效那一段的下标，同原点的两段也不含糊', () => {
  const x = { x: 400, y: 300, w: 600, h: 300 };
  const title = { x: 400, y: 300, w: 300, h: 80 }; // 原点相同、尺寸不同
  const focus = [
    { startMs: 1000, elementId: 'card', rect: x, fadeMs: 240 },
    { startMs: 3000, elementId: 'title', rect: title, fadeMs: 240 }
  ];
  assert.equal(focusMask(focus, 2000)?.index, 0);
  assert.deepEqual(focusMask(focus, 2000)?.rect, x);
  assert.equal(focusMask(focus, 4000)?.index, 1);
  assert.deepEqual(focusMask(focus, 4000)?.rect, title);
  // 换段那一刻 alpha 还是 0（新的一段刚开始淡入），所以返回 null——
  // 此时不该有任何节点亮着，与成片一致。
  assert.equal(focusMask(focus, 3000), null);
});
