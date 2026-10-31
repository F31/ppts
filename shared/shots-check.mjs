// 页内镜头的求值矩阵。
//
// 这些用例不看像素，只看「某一毫秒该显示哪一镜、叠化到什么程度」。它们存在的
// 理由和 motion-check 一样：两端的消费方式不同（DOM 每帧求值、合成器每帧渲染），
// 只要区间端点差一帧，同一时刻就会给出两张不同的图——而那在单端的截图里
// 看不出来，只在两端并排比时露出来。

import assert from 'node:assert/strict';
import test from 'node:test';

import { shotAt, cameraTransform } from './shots.ts';

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

// ---- cameraTransform：与 Go 侧 render.CameraTransform 逐字节同语义 ----
// 覆盖不变量（G7 相机 V 项）的浏览器侧保证：输出中心 MUST 落在 [0.5/scale, 1-0.5/scale]，
// 保证变换后镜头图不露白。两端若有一处不钳制，就会在越界输入下给出两张不同的图。

test('cameraTransform 无关键帧返回默认静止', () => {
  assert.deepEqual(cameraTransform(undefined, 0), { x: 0.5, y: 0.5, scale: 1 });
  assert.deepEqual(cameraTransform([], 1000), { x: 0.5, y: 0.5, scale: 1 });
});

test('cameraTransform 端点钳制（早于首帧 / 晚于末帧）', () => {
  const kfs = [
    { atMs: 0, x: 0.5, y: 0.5, scale: 1 },
    { atMs: 1000, x: 0.5, y: 0.5, scale: 2 },
  ];
  assert.deepEqual(cameraTransform(kfs, -100), { x: 0.5, y: 0.5, scale: 1 });
  assert.deepEqual(cameraTransform(kfs, 99999), { x: 0.5, y: 0.5, scale: 2 });
});

test('cameraTransform 线性插值中点', () => {
  const kfs = [
    { atMs: 0, x: 0.3, y: 0.4, scale: 2 },
    { atMs: 1000, x: 0.7, y: 0.6, scale: 2 },
  ];
  assert.deepEqual(cameraTransform(kfs, 500), { x: 0.5, y: 0.5, scale: 2 });
});

test('cameraTransform scale<1 兜底为 1', () => {
  const kfs = [{ atMs: 0, x: 0.5, y: 0.5, scale: 0.5 }];
  assert.deepEqual(cameraTransform(kfs, 0), { x: 0.5, y: 0.5, scale: 1 });
});

test('cameraTransform clampCenter 覆盖安全区间（与 Go 同语义）', () => {
  // scale=2 安全区间 [0.25,0.75]：越界中心被钳到边界。
  const kfs = [{ atMs: 0, x: 0, y: 1, scale: 2 }];
  assert.deepEqual(cameraTransform(kfs, 0), { x: 0.25, y: 0.75, scale: 2 });
  // scale=1 退化为单点 0.5：任何非零平移都被钳回中心。
  const flat = [{ atMs: 0, x: 0.3, y: 0.8, scale: 1 }];
  assert.deepEqual(cameraTransform(flat, 0), { x: 0.5, y: 0.5, scale: 1 });
  // 合法区间内的中心不钳制。
  const ok = [{ atMs: 0, x: 0.5, y: 0.6, scale: 2 }];
  assert.deepEqual(cameraTransform(ok, 0), { x: 0.5, y: 0.6, scale: 2 });
});

test('cameraTransform NaN 安全（不污染 transform）', () => {
  const nanScale = [{ atMs: 0, x: 0.5, y: 0.5, scale: NaN }];
  const r1 = cameraTransform(nanScale, 0);
  assert.equal(Number.isNaN(r1.scale), false);
  assert.equal(r1.scale, 1);
  // NaN 坐标钳到 lo（scale=2 → lo=0.25）。
  const nanXY = [{ atMs: 0, x: NaN, y: NaN, scale: 2 }];
  const r2 = cameraTransform(nanXY, 0);
  assert.equal(Number.isNaN(r2.x), false);
  assert.equal(Number.isNaN(r2.y), false);
  assert.deepEqual(r2, { x: 0.25, y: 0.25, scale: 2 });
});

test('cameraTransform 覆盖不变量：越界输入下变换后图仍盖住舞台', () => {
  // 对应 Go 侧 TestCameraCoverageInvariant。
  const vW = 1920, vH = 1080;
  for (const sc of [1, 1.5, 2, 3, 4]) {
    const kfs = [
      { atMs: 0, x: 0, y: 0, scale: sc },
      { atMs: 1000, x: 1, y: 1, scale: sc },
    ];
    for (let ms = 0; ms <= 1000; ms += 25) {
      const { x, y, scale } = cameraTransform(kfs, ms);
      assert.equal(Number.isNaN(x), false);
      assert.equal(Number.isNaN(y), false);
      assert.equal(Number.isNaN(scale), false);
      const left = vW * (0.5 - x * scale);
      const right = vW * (0.5 + scale * (1 - x));
      const top = vH * (0.5 - y * scale);
      const bottom = vH * (0.5 + scale * (1 - y));
      assert.ok(left <= 1e-6 && right >= vW - 1e-6 && top <= 1e-6 && bottom >= vH - 1e-6,
        `scale=${sc} ms=${ms}: transform exposes stage edges (left=${left} right=${right} top=${top} bottom=${bottom})`);
    }
  }
});
