#!/usr/bin/env node
// 把成品包定位到指定时刻并截图（供 dom-ssim.py 比对）。
//
// 为什么不沿用「--virtual-time-budget=N 让它自己播到 N」：
//   * 播放器的时钟优先取 audio.currentTime，而虚拟时间跑得比音频快得多
//     （8 秒虚拟时间过去，音频才走了几十毫秒），于是截图总是停在片头；
//   * 就算没有音频、时钟退回 performance.now，落点也只能靠预算估，
//     误差随页数与解码负载变化，无法作为判据的分母。
// 播放器自己有个 seek 滑杆（0–1000‰），点它比等它更准。所以这里是
// 「seek → 等图解码完 → 截图」，落点可读回、可断言。
//
// 用法：
//   node scripts/review/dom-shot.mjs --pkg tmp/pkg --times 0,1200,3700 --out tmp/shots
//   node scripts/review/dom-shot.mjs --pkg tmp/pkg --times 3700 --out tmp/shots --width 1920 --height 1080
// 输出：单行 `SHOT_JSON {...}`，由 dom-ssim.py 解析。
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

import { launchBrowser } from './cdp.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const pkgDir = arg('pkg', '');
const outDir = arg('out', '');
const width = Number(arg('width', '1920'));
const height = Number(arg('height', '1080'));
const times = String(arg('times', ''))
  .split(',')
  .map((s) => s.trim())
  .filter(Boolean)
  .map(Number);

// 页内取一帧：先 seek，再等「当前页图 + 本页层图都 decode 完」。
// 不等解码会拍到空白层——不是播放器的错，是截图抢跑。
const SEEK_FN = `async (ms) => {
  const bar = document.querySelector('.ppts-seek');
  const man = window.__PPT_MANIFEST__;
  if (!bar || !man) return { ok: false, why: 'no-player' };
  const total = man.artifact.durationUs;
  const totalMs = total / 1000;
  // 播放中就先暂停：不停的话，截图的那 100–200ms 里播放器还在往前走，
  // 落点会漂。找不到按钮不算失败——但要把实际落点报回去，由判据决定。
  const pause = [].slice.call(document.querySelectorAll('.ppts-btn'))
    .find((b) => (b.getAttribute('aria-label') || '').indexOf('暂停') >= 0);
  if (pause) pause.click();
  // 滑杆是 0–1000 的千分比，不是毫秒：直接塞毫秒会全部落在 0，
  // 而截图仍然"成功"——只是每一张都在片头。
  bar.value = String(Math.round((ms / totalMs) * 1000));
  bar.dispatchEvent(new Event('input', { bubbles: true }));
  const deadline = Date.now() + 4000;
  const ready = () => {
    const imgs = [].slice.call(document.querySelectorAll('.ppts-slide, .ppts-layer-image'));
    return imgs.length > 0 && imgs.every((im) => !im.getAttribute('src') || (im.complete && im.naturalWidth > 0));
  };
  while (!ready() && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50));
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  // 摘掉外壳（封面按钮的半透明黑底、压在画面底部的字幕条）：比的是「这一页画得
  // 对不对」，不是播放器长什么样。两者各有自己的守卫（V16 验封面能被点掉、
  // 字幕由播放时钟的单测守），留在这里只会把判据淹没在噪音里（实测平均差 28）。
  //
  // 必须 remove 而不是 hidden = true：seek 会走一遍 X()，里面把字幕重新显示出来；
  // 音频元数据迟到时还会再走一次 X()，又一次改回 false。于是同一份包两次运行给出
  // 两种结果（实测 0.9999 与 0.9676 交替出现）。摘掉节点，播放器再改属性也改不回画面。
  for (const sel of ['.ppts-cover', '.ppts-subtitle']) {
    const node = document.querySelector(sel);
    if (node) node.remove();
  }
  await new Promise((r) => requestAnimationFrame(r));
  return {
    ok: ready(),
    paused: !!pause,
    actualMs: Math.round((Number(bar.value) / 1000) * totalMs),
    slides: [].slice.call(document.querySelectorAll('.ppts-slide'))
      .map((im) => ((im.getAttribute('src') || '').split('/').pop())).filter(Boolean),
    layers: document.querySelectorAll('.ppts-layer').length,
    // 舞台在页面里的位置与大小：它是与页图比对的那块区域。
    // 播放器不是全屏铺满（有边框、圆角、控制条），所以不能假定「截图就是画面」。
    stage: (() => {
      const st = document.querySelector('.ppts-stage');
      if (!st) return null;
      const r = st.getBoundingClientRect();
      return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
    })()
  };
}`;

async function main() {
  if (!pkgDir || !outDir || times.length === 0) {
    throw new Error('用法：--pkg DIR --out DIR --times 0,1200,3700');
  }
  const indexFile = path.resolve(pkgDir, 'index.html');
  if (!fs.existsSync(indexFile)) throw new Error(`包里没有 index.html：${indexFile}`);
  fs.mkdirSync(outDir, { recursive: true });

  const b = await launchBrowser({ width, height });
  try {
    await b.S('Page.enable');
    await b.S('Runtime.enable');
    // --window-size 给的是窗口，不是视口：Chrome 会扣掉边框与滚动条（实测 1920×1080
    // 变成 1898×982），截图尺寸于是与页图对不上，比对只能靠缩放糊过去。
    // 显式覆盖设备度量才是"视口就是 1920×1080"。
    await b.S('Emulation.setDeviceMetricsOverride', {
      width, height, deviceScaleFactor: 1, mobile: false,
    });
    // file:// 不能用字符串拼：Windows 上会得到 file://C:\...，Chrome 直接拒收。
    await b.S('Page.navigate', { url: pathToFileURL(indexFile).href });

    // 等播放器建好 DOM（它挂在 DOMContentLoaded 上）。
    let mounted = false;
    for (let i = 0; i < 100 && !mounted; i++) {
      const r = await b.S('Runtime.evaluate', {
        expression: "!!document.querySelector('.ppts-seek') && !!window.__PPT_MANIFEST__",
        returnByValue: true,
      });
      mounted = r.result?.value === true;
      if (!mounted) await sleep(100);
    }
    if (!mounted) throw new Error('播放器没有挂载：找不到 .ppts-seek 或 __PPT_MANIFEST__');

    const viewport = await b.S('Runtime.evaluate', {
      expression: '[window.innerWidth, window.innerHeight, window.devicePixelRatio]',
      returnByValue: true,
    });

    const shots = [];
    for (const t of times) {
      const expr = `(${SEEK_FN})(${t})`;
      const r = await b.S('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) {
        throw new Error(`seek ${t}ms 失败：` + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
      }
      const info = r.result?.value ?? {};
      const file = path.join(outDir, `t${t}.png`);
      const shot = await b.S('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      fs.writeFileSync(file, Buffer.from(shot.data, 'base64'));
      shots.push({ requestedMs: t, file, ...info });
    }

    process.stdout.write('SHOT_JSON ' + JSON.stringify({
      ok: true,
      chrome: b.chromePath,
      viewport: viewport.result?.value ?? null,
      shots,
    }) + '\n');
  } finally {
    b.close();
  }
}

main().then(
  () => process.exit(0),
  (err) => {
    process.stdout.write('SHOT_JSON ' + JSON.stringify({ ok: false, error: String(err && err.message || err) }) + '\n');
    process.exit(1);
  }
);
