// degrade-check —— 单页降级的纯函数门禁（tasks 3.7）。
//
// 这里只测「冻结一页」这件事本身的语义。渲染路径（真实出片、静态重试是否成功）
// 由容器内的故障注入验收覆盖：单页降级不报错，只表现为「这页没有动画」，
// 纯函数测不到、人眼也容易放过。

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DEGRADED_SKIPPED, DEGRADED_STILL, faultPages, stillPage, stillTimeline } from './degrade.mjs';

// 这两个值必须与 internal/render/service.go 的 DegradationStill / DegradationSkipped 一致：
// 上游按 code 分支（page_skipped 才去补静态段）。改值时两侧要一起改，这里先红。
test('降级原因码与 Go 侧常量一致', () => {
  assert.equal(DEGRADED_STILL, 'page_render_failed');
  assert.equal(DEGRADED_SKIPPED, 'page_skipped');
});

const page = (index, clips) => ({
  index,
  slideId: `s${index}`,
  durationMs: 4000,
  baseImage: `pages/p${index}.png`,
  transition: { type: 'fade', durationMs: 400 },
  layers: [{ elementId: `el${index}`, box: { x: 0, y: 0, w: 10, h: 10 }, clips }]
});

test('stillPage 把每条 clip 压成零长，取值为终态', () => {
  const p = page(1, [
    { effect: 'fadeIn', startMs: 0, durationMs: 600, easing: { x1: 0, y1: 0, x2: 1, y2: 1 }, props: [{ name: 'opacity', from: 0, to: 1 }] },
    { effect: 'slideIn', startMs: 600, durationMs: 500, easing: { x1: 0, y1: 0, x2: 1, y2: 1 }, props: [{ name: 'translateY', from: 24, to: 0 }] }
  ]);
  const still = stillPage(p);
  for (const clip of still.layers[0].clips) {
    assert.equal(clip.startMs, 0, '起点归零，任意时刻都已开始');
    assert.equal(clip.durationMs, 0, '零长 → 进度恒为 1（末帧状态）');
  }
  // to 值必须保留：丢了就等于不知道终点在哪。
  assert.deepEqual(
    still.layers[0].clips.map((c) => c.props.map((x) => x.to)),
    [[1], [0]]
  );
});

test('stillPage 不修改入参（服务端可能还要用原计划算别的）', () => {
  const clips = [{ effect: 'fadeIn', startMs: 300, durationMs: 600, easing: {}, props: [{ name: 'opacity', from: 0, to: 1 }] }];
  const p = page(0, clips);
  const before = JSON.stringify(p);
  stillPage(p);
  assert.equal(JSON.stringify(p), before, '原页必须逐字节不变');
  assert.equal(p.layers[0].clips[0].startMs, 300);
});

test('stillTimeline 只冻结目标页，其余页保持原引用', () => {
  const timeline = { schemaVersion: 'v1', fps: 30, width: 1920, height: 1080, pages: [page(0, []), page(1, []), page(2, [])] };
  const frozen = stillTimeline(timeline, 1);
  assert.equal(frozen.pages[0], timeline.pages[0], '第 0 页不该被复制');
  assert.equal(frozen.pages[2], timeline.pages[2], '第 2 页不该被复制');
  assert.notEqual(frozen.pages[1], timeline.pages[1]);
  // 页序与页数不变：frameRange 依赖它，少一页会让后面的页整体错位。
  assert.deepEqual(frozen.pages.map((p) => p.slideId), ['s0', 's1', 's2']);
});

test('stillTimeline 保留转场与页时长', () => {
  const timeline = { fps: 30, width: 1, height: 1, pages: [page(0, []), page(1, [])] };
  const frozen = stillTimeline(timeline, 1);
  assert.deepEqual(frozen.pages[1].transition, { type: 'fade', durationMs: 400 });
  assert.equal(frozen.pages[1].durationMs, 4000);
});

test('零长 clip 交给合成器求值即终态（freeze 手法的全部依据）', async () => {
  // 这是 real dependency：degrade.mjs 靠「durationMs=0 → 进度恒为 1」冻结画面。
  // 若哪天 motion.ts 改掉这段语义，这里会先红，而不是等到成片里元素停在半路中间。
  // 与 web 侧 animation-check 同样的加载方式（Node 22 的 --experimental-strip-types）。
  const { evaluateLayer } = await import('../shared/motion.ts');
  const layer = {
    elementId: 'el',
    kind: 'element',
    assetMode: 'bitmap-region',
    box: { x: 0, y: 0, w: 10, h: 10 },
    clips: [
      { effect: 'fadeIn', startMs: 0, durationMs: 0, easing: { x1: 0, y1: 0, x2: 1, y2: 1 }, props: [{ name: 'opacity', from: 0, to: 1 }] },
      { effect: 'slideIn', startMs: 0, durationMs: 0, easing: { x1: 0, y1: 0, x2: 1, y2: 1 }, props: [{ name: 'translateY', from: 24, to: 0 }] },
      { effect: 'fadeOut', startMs: 0, durationMs: 0, easing: { x1: 0, y1: 0, x2: 1, y2: 1 }, props: [{ name: 'opacity', from: 1, to: 0 }] }
    ]
  };
  const state = evaluateLayer(layer, 0);
  assert.equal(state.opacity, 0, 'fadeOut 元素在静态段里应当已经消失（取 to 值）');
  assert.equal(state.ty, 0, 'slideIn 元素停在终点位置');
});

test('faultPages 忽略空项与非法值（注入开关不该让渲染起不来）', () => {
  assert.deepEqual([...faultPages('1,3')], [1, 3]);
  assert.deepEqual([...faultPages(' 2 , 0 ')], [2, 0]);
  assert.deepEqual([...faultPages('')], []);
  assert.deepEqual([...faultPages(undefined)], []);
  assert.deepEqual([...faultPages('a,-1,2.5,7')], [7], '只接受非负整数');
});
