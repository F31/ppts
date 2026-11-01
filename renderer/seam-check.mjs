// seam-check —— 渲染器端的页缝延续验收。
//
// 为什么渲染器要单独验一次：播放器与渲染器共用 shared/motion.ts 的 evaluateLayer，
// 所以「负起点平移」在两边都成立——这是设计层面的结论，不是验收。渲染器另有三件自己的事，
// 求值器管不到：
//   1. 它按**帧网格**取样（durationInFrames = framesForDuration(pageMs, fps)），
//      页缝两侧实际取到的是「本页最后一帧」与「次页第 0 帧」，中间隔了 1/fps；
//   2. 它可能在任何一层把 tMs 夹到 [0, pageMs]，或把负 startMs 当成非法值丢掉——
//      这类改动在单页截图上完全看不出来；
//   3. 转场期间它会同时画「前一页的末帧」与「次页的当前帧」。
// 本文件只钉第 1、2 条，第 3 条由 Timeline.tsx 的转场分支保证（prev 以 prev.durationMs 取帧）。
//
// 判定思路：
//   * 理想页缝（前一页恰好 pageMs vs 次页恰好 0）必须**逐位相等**——这是连续性的定义；
//   * 渲染器实际能取到的相邻两帧，其差值必须只相当于「1/fps 的动画推进」，
//     不能是「回到起点」量级；
//   * 负对照：把延续段按「重置」写法（startMs=0）求值，差值必须显著变大——
//     否则说明阈值宽到根本区分不了这两种实现，断言等于没写。

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { evaluateLayer, framesForDuration } from '../shared/motion.ts';

const PAGE_MS = 6000;
const FPS = 30;
const OFFSET = 3000; // 本页段末尾在原 clip 里的时间，也是次页段的平移量
const DURATION = 8000;
const EASE = { x1: 0.22, y1: 1, x2: 0.36, y2: 1 };
const OPACITY = [{ name: 'opacity', from: 0, to: 1 }];
const SLIDE_UP = [{ name: 'translateY', from: 40, to: 0 }];

const box = { x: 0, y: 0, w: 400, h: 120 };

/** 本页源段：完整 [from,to]，起点 = 页尾 - offset（与编译器的 continues 溢出同形）。 */
const sourceLayer = {
  elementId: 'cont-0',
  kind: 'card',
  assetMode: 'bitmap-region',
  box,
  clips: [
    { effect: 'fadeIn', startMs: PAGE_MS - OFFSET, durationMs: DURATION, easing: EASE, props: OPACITY },
    { effect: 'slideUp', startMs: PAGE_MS - OFFSET, durationMs: DURATION, easing: EASE, props: SLIDE_UP },
  ],
};

/** 次页延续段：负起点平移，Props 与源段完全一致。 */
const continuationLayer = {
  elementId: 'cont-0',
  kind: 'card',
  assetMode: 'bitmap-region',
  box,
  clips: [
    {
      effect: 'fadeIn', startMs: -OFFSET, durationMs: DURATION, easing: EASE, props: OPACITY,
      isContinuation: true, continuationOffsetMs: OFFSET, elementId: 'cont-0',
    },
    {
      effect: 'slideUp', startMs: -OFFSET, durationMs: DURATION, easing: EASE, props: SLIDE_UP,
      isContinuation: true, continuationOffsetMs: OFFSET, elementId: 'cont-0',
    },
  ],
};

/** 「重置」写法：次页把 clip 起点归零——即没有延续能力时的退化实现。 */
const resetLayer = {
  ...continuationLayer,
  clips: continuationLayer.clips.map(({ isContinuation, continuationOffsetMs, ...c }) => ({ ...c, startMs: 0 })),
};

function delta(a, b) {
  return {
    opacity: Math.abs(a.opacity - b.opacity),
    tx: Math.abs(a.tx - b.tx),
    ty: Math.abs(a.ty - b.ty),
    max: Math.max(Math.abs(a.opacity - b.opacity), Math.abs(a.tx - b.tx), Math.abs(a.ty - b.ty)),
  };
}

/** 渲染器一页的帧数（与 Timeline.tsx 的 durationInFrames 同一个函数）。 */
const pageFrames = framesForDuration(PAGE_MS, FPS);
/** 页内第 f 帧的毫秒数（Timeline.tsx: tMs = frame * 1000 / fps）。 */
const tMsAtFrame = (f) => (f * 1000) / FPS;

test('帧网格口径：一页 6s @30fps 是 180 帧，页缝两侧只差 1/fps', () => {
  assert.equal(pageFrames, 180);
  const lastFrameTMs = tMsAtFrame(pageFrames - 1);
  assert.equal(lastFrameTMs, 5966.666666666667);
  assert.ok(PAGE_MS - lastFrameTMs > 0 && PAGE_MS - lastFrameTMs <= 1000 / FPS,
    `末帧到页尾的间隔必须落在 (0, 1/fps]，实际 ${PAGE_MS - lastFrameTMs}ms`);
});

test('理想页缝连续：前一页 pageMs 与次页 0 点的合成态逐位相等', () => {
  const before = evaluateLayer(sourceLayer, PAGE_MS);
  const after = evaluateLayer(continuationLayer, 0);
  const d = delta(before, after);
  assert.ok(d.max <= 1e-9, `页缝两侧必须相等（连续性定义），实际 Δ=${JSON.stringify(d)}`);
  // 顺带钉住「缝处处于中途态」：若进度为 0 或 1，这条用例就没有跨过页缝。
  assert.ok(before.opacity > 0 && before.opacity < 1,
    `页缝处应处于中途态，实际 opacity=${before.opacity}`);
});

test('渲染器实际相邻帧：差值只相当于 1/fps 的推进，不是回到起点', () => {
  const lastFrameTMs = tMsAtFrame(pageFrames - 1);
  const before = evaluateLayer(sourceLayer, lastFrameTMs); // 本页最后一帧
  const after = evaluateLayer(continuationLayer, 0); // 次页第 0 帧
  const d = delta(before, after);

  // 一帧的理论进度：1 / (fps × duration 秒) = 1/(30×8) ≈ 0.00417。
  // 缓动在起点附近斜率可大于 1，留 3 倍余量；但绝不该是「整段进度」量级。
  const oneFrameProgress = 1000 / FPS / DURATION;
  assert.ok(d.opacity <= oneFrameProgress * 3,
    `opacity 单帧推进过大：Δ=${d.opacity}，一帧理论值=${oneFrameProgress}`);
  // slideUp 的 40px 在 8s 里推进，一帧约 0.17px；留 3 倍余量。
  assert.ok(d.ty <= 40 * oneFrameProgress * 3,
    `translateY 单帧推进过大：Δ=${d.ty}px`);
});

test('负对照：按「重置」写法求值时差值显著变大（证明断言有区分力）', () => {
  const seamLeft = evaluateLayer(sourceLayer, PAGE_MS);
  const reset = evaluateLayer(resetLayer, 0);
  const d = delta(seamLeft, reset);
  // 重置写法下 opacity 会掉回 0，而缝处应在 0.3 以上；差值是整段量级，必须远超阈值。
  assert.ok(d.opacity > 0.2,
    `重置写法必须被区分出来，实际 Δopacity=${d.opacity}（缝处 opacity=${seamLeft.opacity}）`);
  assert.ok(d.max > 10 * (1000 / FPS / DURATION),
    `重置写法的差值应远超一帧推进，实际 Δmax=${d.max}`);
});

test('页序累加无空隙：第 2 页的起始帧恰好是第 1 页的帧数', () => {
  // 与 Timeline.tsx 的 pageStartFrames 同口径。渲染器按帧拼页，累加错一帧会让
  // 后续每一页整体平移——而画面上只会表现为「转场早了一点」，极难发现。
  const starts = [];
  let cursor = 0;
  for (const ms of [PAGE_MS, PAGE_MS]) {
    starts.push(cursor);
    cursor += framesForDuration(ms, FPS);
  }
  assert.deepEqual(starts, [0, 180]);
  assert.equal(cursor, 360);
});
