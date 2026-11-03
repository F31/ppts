#!/usr/bin/env node
/**
 * player-freeze-check —— 「暂停之后画面真的不动了」。
 *
 * 修的是这样一类现场：截图门禁请求 1967ms 那一帧，拍到的却是 1985ms。
 * 差 18ms 不到一帧，看起来像"浏览器抖了一下"，实际是播放器自己的竞态：
 *
 *   audio.play() 是异步的。首屏自动播放发出后、Promise 落定前，音频管线已经在走
 *   （currentTime 在涨），而按钮文案还是「播放」。此刻按暂停只能把 playing 置回
 *   false，随后那次 play() 的 then 回调又把 playing 置回 true 并重启渲染循环。
 *   于是"暂停"在几百毫秒后自己失效，画面继续往前走。
 *
 * 截图探针当时正是靠 aria-label 找暂停按钮的：文案还没切，它找不到按钮，就报告
 * "没在播"、不暂停、直接截图。同一毫秒级的竞争也解释了 CI 上两条门禁的偶发红
 * （V18 落点偏差 16ms；V13 区域 SSIM 0.9388 因为拍在了叠化窗口的另一侧）。
 *
 * 这里测纯函数（凭据语义）；播放器里的接线（pause 先作废、自动播放也领号）
 * 用源码断言守——光有函数没接上，等于没修。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const { createPlayGuard } = await import('./src/sharePlayer/playGuard.ts');
const player = readFileSync(new URL('./src/sharePlayer/main.ts', import.meta.url), 'utf8');

test('正常路径：刚领的号有效', () => {
  const g = createPlayGuard();
  assert.equal(g.accept(g.begin()), true);
});

test('暂停作废在途尝试：pause 之后那次 play 的落定不得接管', () => {
  const g = createPlayGuard();
  const autoplay = g.begin();
  g.cancel();
  assert.equal(g.accept(autoplay), false, '暂停已发生，这次自动播放的结果必须被丢弃');
});

test('后来的 play 让先前的作废：两次尝试不会同时接管', () => {
  const g = createPlayGuard();
  const first = g.begin();
  const second = g.begin();
  assert.equal(g.accept(first), false);
  assert.equal(g.accept(second), true);
});

test('作废不会毒化后续尝试：暂停后再播仍然算数', () => {
  const g = createPlayGuard();
  g.cancel();
  g.cancel();
  assert.equal(g.accept(g.begin()), true, '用户暂停后又点了播放，这次必须生效');
});

test('播放器接线：pause 先作废号，自动播放也要领号', () => {
  // 顺序错了就白改：先 pause 再 cancel，那次 play 仍可能在 cancel 之前落定。
  assert.match(
    player,
    /const pause = \(\): void => \{[\s\S]{0,120}?playGuard\.cancel\(\);/,
    'pause() 必须先 playGuard.cancel() 再停，否则在途尝试会在暂停之后接管'
  );
  assert.match(player, /const attempt = playGuard\.begin\(\);/,
    '首屏自动播放必须领号');
  assert.match(player, /const autoplay = audio\.play\(\);/,
    '自动播放仍要真的发起 play()');
  assert.match(player, /if \(!playGuard\.accept\(attempt\)\) return;/,
    '自动播放的两条回执都必须先验收凭据');
});

test('自动播放窗口里的状态必须是真的：先置在播，再发起 play', () => {
  // 这条守的是探针（以及界面）看到的「有没有在播」。发出 play() 之后音频就在走了，
  // 此时若 playing 仍是 false、按钮还写着「播放」，界面在撒谎：
  // 探针据此不暂停就截图，用户点按钮会再发一次 play()。
  assert.match(
    player,
    /const attempt = playGuard\.begin\(\);\s*playing = true;[\s\S]{0,200}?const autoplay = audio\.play\(\);/,
    'autoplay 必须在发起 play() 之前把 playing 置为 true'
  );
  assert.match(
    player,
    /playBtn\.setAttribute\('aria-label', '暂停'\);\s*const autoplay = audio\.play\(\);/,
    '按钮文案要和 playing 同步，否则按文案判断状态的探针会看错'
  );
});

test('探针需要的冻结入口必须暴露', () => {
  assert.match(player, /window\.__PPT_PAUSE__ = pause;/, '没有 __PPT_PAUSE__，探针只能按按钮文案猜');
  assert.match(player, /window\.__PPT_PLAYING__ = \(\) => playing;/, '探针要能读回"是否在播"，而不是从事件推断');
});
