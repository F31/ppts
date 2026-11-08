#!/usr/bin/env node
/**
 * player-fps-check —— 成品包播放器的「实测帧率降级」（设计 §20.10 降级矩阵第四条）。
 *
 * 这一条与 deviceMemory 那条不是一回事：deviceMemory 回答"这台机器有多少内存"，
 * 它判不出「8GB 但核显孱弱 / 同时开着会议与 IDE」的机器。而这类机器上，
 * 30fps 的合成层动画会掉到 10–15fps，看起来像"视频卡了"，不像"该降级"。
 *
 * 判据分两层：
 *  - 纯函数：窗口 fps 的算法、长间隔重置、连续低帧才降级；
 *  - 源码接线：降级真的发生在渲染循环里，且与 lowPower 共用同一条"不建元素层"路径，
 *    降级后页面与字幕仍在（是"有声幻灯"，不是黑屏）。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/sharePlayer/fps.ts');
const player = readFileSync(new URL('./src/sharePlayer/main.ts', import.meta.url), 'utf8');

/** 按固定帧率喂 n 帧，返回最后一次非 null 的采样。 */
function feed(meter, fps, count, startMs = 0) {
  let last = null;
  const step = 1000 / fps;
  for (let i = 0; i < count; i += 1) {
    const sample = meter.frame(startMs + i * step);
    if (sample !== null) last = sample;
  }
  return last;
}

test('窗口攒满才算一次 fps，且算法不系统性高估', () => {
  const meter = new m.FpsMeter();
  // 第一帧只是开窗，不产生读数。
  assert.equal(meter.frame(0), null);
  // 30fps、2s 窗口 = 60 个间隔，读数应落在 30 附近而不是 31（除帧数会高估）。
  const fps = feed(meter, 30, 62);
  assert.ok(fps !== null, '2s 之后必须给出一个读数');
  assert.ok(Math.abs(fps - 30) < 1, `30fps 应读到 30 附近，实际 ${fps}`);
});

test('只跑半秒不降级：窗口没攒满就没有读数', () => {
  const meter = new m.FpsMeter();
  assert.equal(feed(meter, 10, 6), null, '300ms 的低帧不能立刻判死');
});

test('20fps 连续两个窗口才降级，健康窗口清零', () => {
  // 第一窗口 20fps → 一个低帧计数。
  let streak = 0;
  streak = m.nextFpsStreak(streak, 20);
  assert.equal(streak, 1);
  assert.equal(m.shouldDegradeFps(streak), false, '一个窗口不够：首屏解码与切页重建都会抖一下');
  // 第二窗口仍低 → 降级。
  streak = m.nextFpsStreak(streak, 18);
  assert.equal(m.shouldDegradeFps(streak), true);
  // 中间来一个健康窗口 → 计数归零，迟滞必须明确。
  assert.equal(m.nextFpsStreak(streak, 30), 0);
  assert.equal(m.shouldDegradeFps(m.nextFpsStreak(0, 30)), false);
});

test('阈值是 24fps：24 本身不降，23.9 才降', () => {
  assert.equal(m.FPS_DEGRADE_THRESHOLD, 24);
  let streak = 0;
  streak = m.nextFpsStreak(streak, 24);
  streak = m.nextFpsStreak(streak, 24);
  assert.equal(m.shouldDegradeFps(streak), false, '24fps 不算低帧');
  streak = m.nextFpsStreak(0, 23.9);
  streak = m.nextFpsStreak(streak, 23.9);
  assert.equal(m.shouldDegradeFps(streak), true);
});

test('长间隔视为「被打断」：不会把切后台的停顿算成 0.01fps', () => {
  const meter = new m.FpsMeter();
  // 先攒 1s 的 30fps 帧。
  feed(meter, 30, 30);
  // 切走 3s 再回来：这一帧必须丢掉旧窗口，返回 null。
  assert.equal(meter.frame(1000 + 3000), null, '长间隔应重新开窗');
  // 重新开窗后仍要攒满一个完整窗口才有读数。
  assert.equal(feed(meter, 30, 5, 4000), null);
  const fps = feed(meter, 30, 70, 4000 + (5 * 1000) / 30);
  assert.ok(fps !== null && Math.abs(fps - 30) < 1, `打断后应重新读到 30 附近，实际 ${fps}`);
});

test('reset 丢掉半截窗口（暂停/拖动后不拿旧帧凑数）', () => {
  const meter = new m.FpsMeter();
  feed(meter, 30, 30);
  meter.reset();
  assert.equal(feed(meter, 30, 10, 2000), null, 'reset 之后要重新攒满窗口');
});

test('降级接在渲染循环里，且与 lowPower 共用「不建元素层」路径', () => {
  assert.match(player, /import \{ FpsMeter, nextFpsStreak, shouldDegradeFps \} from '\.\/fps';/);
  // 采样必须在 render 之后：这一帧的成本已经付过。
  const renderAt = player.indexOf('    render(us);');
  const sampleAt = player.indexOf('fpsMeter.frame(performance.now())');
  assert.ok(renderAt > 0 && sampleAt > renderAt, '帧率采样要放在 render 之后');
  // 换页重建层时，降级与低内存走同一个分支。
  assert.match(
    player,
    /pickStageLayers\(layers, slide\.focus, lowPower \|\| degraded\)/,
    '降级后新页也不得再建元素层'
  );
  // 已建的层要当场拆掉。
  // 只截 degradeForFps 的函数体：后面的镜头/字幕代码会误伤"没碰 image/subtitle"的断言。
  const degradeBody = /const degradeForFps = \(fps: number\): void => \{[\s\S]*?\n  \};/.exec(player);
  assert.ok(degradeBody, '找不到 degradeForFps 的函数体');
  const degrade = degradeBody[0];
  assert.match(degrade, /releaseLayers\(activeLayers\)/, '降级必须立刻释放已建的层');
  // 只降动画、不降画面与字幕：degradeForFps 里不许碰 image / subtitle / audio。
  assert.doesNotMatch(degrade, /image\.|subtitle|audio\./, '降级只拆动画层，页面、音频与字幕必须保留');
});

test('降级是单向的：中途恢复动效比一直不动更像故障', () => {
  const assignments = player.match(/degraded = (true|false)/g) ?? [];
  assert.deepEqual(assignments, ['degraded = false', 'degraded = true'], '只允许初始化一次、置位一次');
  // 置位之后不得再被读成 false 的路径覆盖：只降不升。
  assert.match(player, /if \(!degraded && fps !== null\)/, '降级后不再采样/不再重复降级');
});

test('阈值不硬编码在播放器里', () => {
  assert.doesNotMatch(player, /fps\s*[<>]=?\s*24\b/, '阈值必须来自 ./fps，改一处就要全生效');
  assert.doesNotMatch(player, /streak\s*>=\s*2\b/, '迟滞窗口数也必须来自 ./fps');
});
