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
// 舞台与视口的关系（--stage）：
//   播放器把卡片宽度钉在 1280（`max-width:1280px`），舞台跟着卡片走。于是 1920 的
//   页在浏览器里只会以 1280 画出，而 MP4 是 1920——两者比 SSIM 时掺进来的重采样误差
//   分不清「画错了」和「只是缩放了」（实测 0.969 vs 0.99995）。
//   卡片的宽度上限是**网页嵌用**的策略，不是画面本身的性质：层的位置全按画布百分比
//   落位（animation.ts 的 pct()），去掉上限只会让同一页按原生尺寸画一遍。
//   所以 --stage WxH 做两件事：把视口按「舞台 + 边距」定好，再去掉那道上限，
//   最后**校验实测舞台就是 W×H**——不是「大致对」，是等号。
//
// 用法：
//   node scripts/review/dom-shot.mjs --pkg tmp/pkg --times 0,1200,3700 --out tmp/shots
//   node scripts/review/dom-shot.mjs --pkg tmp/pkg --times 3700 --out tmp/shots --stage 1920x1080
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
const stageArg = String(arg('stage', '')).trim();
const stageWanted = /^\d+x\d+$/.test(stageArg)
  ? stageArg.split('x').map(Number)
  : null;
// body 的左右内边距各 24（见 player.css），上下留出控制条与卡片阴影的余量：
// 视口比舞台高 160，卡片才不会被 flex 居中顶出画面。
const width = stageWanted ? stageWanted[0] + 48 : Number(arg('width', '1920'));
const height = stageWanted ? stageWanted[1] + 160 : Number(arg('height', '1080'));
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
  //
  // 千分比的量化步长 = 总时长/1000，长作业上一步就是几秒，而门禁要取
  // 「切点前后一帧」（33ms）。所以播放器暴露了微秒级定位，有它就用它；
  // 没有时退回滑杆，并把量化误差如实报回去（由判据决定要不要接受）。
  const precise = typeof window.__PPT_SEEK_US__ === 'function';
  if (precise) window.__PPT_SEEK_US__(ms * 1000);
  else {
    bar.value = String(Math.round((ms / totalMs) * 1000));
    bar.dispatchEvent(new Event('input', { bubbles: true }));
  }
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
  // 当前面板的结构化状态。V18（切图判据）要回答三件事：此刻显示的是哪张图、
  // 叠化时两张各可见多少、层有没有被重建——三件都只能从**当前面板**读。
  // 舞台上有两个面板（上一页末帧 + 当前页），读错一个会得到看似合理的答案。
  const probePanel = (() => {
    const panels = [].slice.call(document.querySelectorAll('.ppts-panel')).filter((p) => !p.hidden);
    const panel = panels[panels.length - 1];
    if (!panel) return null;
    const imgs = [].slice.call(panel.querySelectorAll('.ppts-slide'));
    const box = panel.querySelector('.ppts-layers');
    window.__ppsProbeSeq = window.__ppsProbeSeq || 0;
    return {
      // 按 DOM 顺序：底层是当前镜，顶层只在叠化窗口内出现。
      images: imgs.map((im) => {
        const raw = im.style.opacity;
        return {
          src: (im.getAttribute('src') || '').split('/').pop(),
          hidden: !!im.hidden,
          opacity: raw ? Number(raw) : Number(getComputedStyle(im).opacity),
        };
      }),
      layers: panel.querySelectorAll('.ppts-layer').length,
      // 层节点的身份。页内切镜 MUST 不重建层（重建会让元素重新入场，clips 的
      // 页内相对时间也会跟着错位）。只看层数分辨不出重建——重建前后都是 2 个，
      // 所以给每个节点打一个只打一次的标记：重建出的新节点没有标记，会拿到新号。
      layerIds: box
        ? [].slice.call(box.children).map((node) => {
            if (!node.dataset.ppsProbe) node.dataset.ppsProbe = String(++window.__ppsProbeSeq);
            return node.dataset.ppsProbe;
          }).join(',')
        : '',
      layersVisible: box ? getComputedStyle(box).visibility !== 'hidden' : false,
    };
  })();
  return {
    ok: ready(),
    paused: !!pause,
    // 这次定位是靠微秒钩子还是靠千分比滑杆。报告里带出来——两者的落点精度
    // 差两个数量级，混在一起看会让人误以为滑杆也够用。
    precise,
    // 精确钩子在读的时候不受千分比量化影响；没有钩子时只能读滑杆。
    actualMs: Math.round(
      precise && typeof window.__PPT_POSITION_US__ === 'function'
        ? window.__PPT_POSITION_US__() / 1000
        : (Number(bar.value) / 1000) * totalMs
    ),
    slides: [].slice.call(document.querySelectorAll('.ppts-slide'))
      .map((im) => ((im.getAttribute('src') || '').split('/').pop())).filter(Boolean),
    layers: document.querySelectorAll('.ppts-layer').length,
    panel: probePanel,
    // 兼容旧调用方：层可见性单独给一份，语义与 probePanel.layersVisible 相同。
    layersVisible: !!probePanel && probePanel.layersVisible,
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

    // 去掉卡片宽度上限，让舞台按原生尺寸画。不给 --stage 时不动它——那是
    // 「用户双击打开看到的样子」，V16/V17 验的正是那个形态。
    if (stageWanted) {
      const r = await b.S('Runtime.evaluate', {
        expression: `(() => {
          const card = document.querySelector('.ppts-share');
          if (!card) return { ok: false, why: 'no-card' };
          card.style.maxWidth = 'none';
          const st = document.querySelector('.ppts-stage');
          if (!st) return { ok: false, why: 'no-stage' };
          const box = st.getBoundingClientRect();
          return { ok: true, w: box.width, h: box.height, y: box.y, vh: window.innerHeight };
        })()`,
        returnByValue: true,
      });
      const got = r.result?.value || {};
      if (!got.ok) throw new Error(`无法确定舞台尺寸：${got.why}`);
      const [wantW, wantH] = stageWanted;
      // 用原始浮点数比，不用四舍五入：差 0.4px 也是没对上，而它会被 round 吞掉。
      if (Math.abs(got.w - wantW) >= 0.5 || Math.abs(got.h - wantH) >= 0.5) {
        throw new Error(
          `舞台实测 ${got.w.toFixed(2)}×${got.h.toFixed(2)}，要的是 ${wantW}×${wantH}（视口 ${width}×${height}）`
        );
      }
      if (got.y < 0 || got.y + got.h > got.vh) {
        throw new Error(`舞台超出视口：y=${got.y} h=${got.h.toFixed(1)} 视口高=${got.vh}`);
      }
    }

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
