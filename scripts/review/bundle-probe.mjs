#!/usr/bin/env node
// 成品包质量探针（V16 可播放性 + V17 降级矩阵的执行体）。
//
// 为什么是 CDP 而不是 `--dump-dom` + 虚拟时间：
//   - 虚拟时间能让 setTimeout 飞速跑完，适合截图定位某一帧（dom-ssim.py 就这么用），
//     但媒体播放速度是真的 1x —— 虚拟时间跑完 8 秒，音频才走几十毫秒，ended 永不触发。
//   - 播放器用 `createElement('audio')` 建了元素却不 append 到 DOM，页面里
//     `querySelector('audio')` 永远是 null。只有在页面脚本执行**之前** hook
//     `HTMLMediaElement.prototype.play` 才拿得到它，这正是 addScriptToEvaluateOnNewDocument 的时机。
//   - 注入的脚本走 DevTools 特权上下文，不受产物 CSP 限制（CSP 是 `script-src 'self' file:`，
//     往 index.html 里塞内联 <script> 会被直接拒绝）。
//
// 用法：node bundle-probe.mjs --pkg /pkg --mode play|nojs|nowaapi|reduced|lowmem
//       node bundle-probe.mjs --print-chrome        # 只打印解析到的浏览器路径，供门禁选引擎
// 输出：单行 `PROBE_JSON {...}`，由 bundle-gate.py 解析判定。
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// 探针原先只在渲染镜像里跑（Chrome 在 /opt/chrome，ws 在 /app/node_modules），
// 于是「能不能验成品包」被绑在「镜像能不能构建」上。现在改成两条路都能走：
// 镜像内照旧；镜像外只要有 Chrome，用 Node 22 自带的 WebSocket 即可，不必装 ws。
const CHROME_CANDIDATES = [
  '/opt/chrome/chrome-headless-shell-linux64/chrome-headless-shell',
  '/usr/bin/google-chrome',
  '/usr/bin/google-chrome-stable',
  '/opt/google/chrome/chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
];

function resolveChrome() {
  // 显式指定优先：写错了要报错，而不是悄悄退到下一个候选——
  // 「我明明设了 PPT_CHROME 却没生效」比「找不到 Chrome」难查得多。
  if (process.env.PPT_CHROME) {
    if (!fs.existsSync(process.env.PPT_CHROME)) {
      throw new Error(`PPT_CHROME 指向的文件不存在：${process.env.PPT_CHROME}`);
    }
    return process.env.PPT_CHROME;
  }
  const hit = CHROME_CANDIDATES.find((p) => fs.existsSync(p));
  if (!hit) throw new Error('no chrome binary: 设置 PPT_CHROME 指向 chrome/chrome-headless-shell');
  return hit;
}

if (process.argv.includes('--print-chrome')) {
  try {
    process.stdout.write(resolveChrome() + '\n');
    process.exit(0);
  } catch (err) {
    process.stderr.write(String(err.message) + '\n');
    process.exit(2);
  }
}

const CHROME = resolveChrome();
const DEFAULT_PKG = '/pkg';
const PORT_BASE = 9300;
const HEADLESS_SHELL = /headless/i.test(path.basename(CHROME));

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const pkgDir = arg('pkg', DEFAULT_PKG);
const mode = arg('mode', 'play');
const port = Number(arg('port', String(PORT_BASE + Math.floor(Math.random() * 400))));

const HOOK = `
window.__probe = { errors: [], media: [] };
addEventListener('error', function (e) {
  var t = e.target;
  if (t && t.tagName) {
    __probe.errors.push('RES:' + t.tagName + ':' + String(t.src || t.href || '').split('/').pop());
  } else {
    __probe.errors.push('JS:' + (e.message || ''));
  }
}, true);
addEventListener('unhandledrejection', function (e) { __probe.errors.push('REJ:' + String(e.reason)); });
(function () {
  var play = HTMLMediaElement.prototype.play;
  HTMLMediaElement.prototype.play = function () {
    if (__probe.media.indexOf(this) < 0) __probe.media.push(this);
    return play.apply(this, arguments);
  };
})();
`;

const HOOK_NO_WAAPI = `Element.prototype.animate = undefined;`;

const HOOK_LOWMEM = `
(function () {
  try {
    Object.defineProperty(Navigator.prototype, 'deviceMemory', { configurable: true, get: function () { return 2; } });
  } catch (e) { window.__probeLowMemFailed = String(e); }
})();
`;

// 页面内执行的检查体。返回普通对象，由 node 侧序列化。
const PLAY_EXPR = `(async () => {
  const out = { mounted: false, layers: 0, resources: { checked: 0, failed: [], missing: [] }, audio: null, ended: null, pageErrors: [] };
  const stage = document.querySelector('.ppts-stage');
  out.mounted = !!stage;
  out.layers = document.querySelectorAll('.ppts-layer').length;
  out.pageErrors = (window.__probe && __probe.errors) || [];
  out.subtitlesLoaded = Array.isArray(window.__PPT_SUBTITLES__);
  const man = window.__PPT_MANIFEST__;
  if (!man) { out.missing.push('manifest'); return out; }

  // 清单里被播放器引用的全部资源：页图、层图、音频。播放器只会加载当前页与当前段，
  // 其余的 404 平时永远发现不了，所以这里逐个预加载。
  const urls = [];
  const push = (u) => { if (u && urls.indexOf(u) < 0) urls.push(u); };
  (man.slides || []).forEach((s) => {
    push(s.image);
    (s.layers || []).forEach((l) => push(l.image));
  });
  if (man.audioMix) push(man.audioMix);
  (man.audio || []).forEach((a) => push(a.src));

  const loadOne = (url) => new Promise((resolve) => {
    const isAudio = /\\.(wav|mp3|m4a|aac|ogg)$/i.test(url);
    const node = isAudio ? document.createElement('audio') : new Image();
    const timer = setTimeout(() => resolve({ url, ok: false, why: 'timeout' }), 8000);
    const done = (ok, why) => { clearTimeout(timer); resolve({ url, ok, why }); };
    node.onload = () => done(true);
    node.onerror = () => done(false, 'error');
    if (isAudio) {
      node.onloadedmetadata = () => done(true);
      node.onerror = () => done(false, 'error');
    }
    node.src = url;
  });

  const results = await Promise.all(urls.map(loadOne));
  out.resources.checked = results.length;
  out.resources.failed = results.filter((r) => !r.ok).map((r) => r.url + (r.why === 'timeout' ? ' (timeout)' : ''));

  const a = (window.__probe && __probe.media && __probe.media[0]) || null;
  if (a) {
    out.audio = {
      src: String(a.currentSrc || a.src || '').split('/').pop(),
      durationSec: isFinite(a.duration) ? a.duration : -1,
      readyState: a.readyState,
      manifestSec: (man.artifact && man.artifact.durationUs ? man.artifact.durationUs : 0) / 1e6
    };
    const waitFor = (ev, ms) => new Promise((resolve) => {
      const t = setTimeout(() => resolve('timeout'), ms);
      a.addEventListener(ev, () => { clearTimeout(t); resolve(ev); }, { once: true });
    });
    a.pause();
    try {
      // 只播末尾 1.5 秒：验的是「能不能走到 ended」，不是「30 秒音频是否流畅」。
      // 6 倍速让真实等待压到 0.25 秒，避免门禁跑满整个片长。
      a.playbackRate = 6;
      a.currentTime = Math.max(0, a.duration - 1.5);
    } catch (e) { out.pageErrors.push('SEEK:' + String(e)); }
    // 到达末尾有两条路：媒体元素触发 ended，或播放器自己的 tick 在
    // us >= durationUs 时 finish() 并 pause ——后者会抢在 ended 之前暂停音频，
    // 于是 ended 永远不来。两种都算"播到了结尾"，判定时用 OR，
    // 否则门禁会因为一个与用户无关的竞态偶尔红一次（这类间歇红最终会被人关掉）。
    const endedP = waitFor('ended', 12000);
    const reachedP = (async () => {
      for (let i = 0; i < 120; i++) {
        if (a.currentTime >= a.duration - 0.05) return 'reached-end';
        await new Promise((r) => setTimeout(r, 100));
      }
      return 'timeout';
    })();
    try { await a.play(); } catch (e) { out.pageErrors.push('PLAY:' + String(e)); }
    const first = await Promise.race([endedP, reachedP]);
    out.ended = first;
    out.paused = a.paused;
    out.finalTimeSec = a.currentTime;
  }
  return out;
})()`;

const NOJS_EXPR = `(() => {
  const box = document.querySelector('.ppts-noscript');
  const images = [].map.call(document.querySelectorAll('.ppts-noscript-image'), (img) => ({
    src: String(img.getAttribute('src') || ''),
    width: img.naturalWidth
  }));
  const texts = [].map.call(document.querySelectorAll('.ppts-noscript-text'), (n) => (n.textContent || '').trim());
  return {
    hasNoscript: !!box,
    imageCount: images.length,
    loaded: images.filter((i) => i.width > 0).length,
    images,
    textCount: texts.filter((t) => t.length > 0).length,
    note: ((document.querySelector('.ppts-noscript-note') || {}).textContent || '').trim().slice(0, 80),
    stageCreated: !!document.querySelector('.ppts-stage')
  };
})()`;

// 层样式按「每页开场 400ms」采样：本项目的入场动画都在页首 100–800ms 内完成，
// 取页中或页尾时所有层都已停在终态（identity + opacity 1）——
// 在那里比对会得出"默认态也没有位移"的假象，reduced-motion 的对照就失去意义。
const LAYER_EXPR = `(async () => {
  const bar = document.querySelector('.ppts-seek');
  const man = window.__PPT_MANIFEST__;
  const out = {
    samples: [],
    hasStage: !!document.querySelector('.ppts-stage'),
    lowMemHookFailed: window.__probeLowMemFailed || null
  };
  const snap = () => {
    const nodes = [].slice.call(document.querySelectorAll('.ppts-layer'));
    return nodes.map((n) => {
      const cs = getComputedStyle(n);
      const anims = n.getAnimations ? n.getAnimations() : [];
      let durationMs = -1;
      if (anims.length) { try { durationMs = anims[0].effect.getTiming().duration; } catch (e) { durationMs = -1; } }
      return { transform: cs.transform, opacity: Number(cs.opacity), animations: anims.length, durationMs };
    });
  };
  const total = man && man.artifact ? man.artifact.durationUs : 0;
  const stops = [];
  if (man && total > 0) {
    man.slides.forEach((s) => {
      stops.push({ at: s.slideId + '+400ms', us: Math.min(s.endUs, s.startUs + 400000) });
    });
  }
  for (const stop of stops) {
    if (bar) {
      bar.value = String(Math.round((stop.us / total) * 1000));
      bar.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise((r) => setTimeout(r, 260));
    }
    out.samples.push({ at: stop.at, layers: snap() });
  }
  out.slideSrc = ((document.querySelector('.ppts-slide') || {}).currentSrc || '').split('/').pop();
  return out;
})()`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// CDP 通道：Node 22 自带 WebSocket（undici）足够用；旧 Node 才退回镜像里的 ws。
// 两条路都收成同一组方法，调用处不必知道用的是哪个。
async function openSocket(url) {
  if (typeof globalThis.WebSocket === 'function') {
    const ws = new WebSocket(url);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true });
      ws.addEventListener('error', () => reject(new Error('websocket 握手失败')), { once: true });
    });
    return {
      send: (text) => ws.send(text),
      onMessage: (fn) => ws.addEventListener('message', (ev) => fn(typeof ev.data === 'string' ? ev.data : String(ev.data))),
      close: () => { try { ws.close(); } catch { /* 已关闭 */ } }
    };
  }
  const { createRequire } = await import('node:module');
  const require = createRequire(path.join(process.env.PPT_WS_ROOT || '/app', 'package.json'));
  const Ws = require('ws');
  const ws = new Ws(url, { perMessageDeflate: false, maxPayload: 256 * 1024 * 1024 });
  await new Promise((resolve, reject) => { ws.on('open', resolve); ws.on('error', reject); });
  return {
    send: (text) => ws.send(text),
    onMessage: (fn) => ws.on('message', (raw) => fn(raw.toString())),
    close: () => { try { ws.close(); } catch { /* 已关闭 */ } }
  };
}

// 失败路径也要收摊：探针被门禁调用时，留下一个 Chrome 进程会让下一次运行抢不到端口。
let cleanupAll = () => {};

async function main() {
  // 独立的用户数据目录：不指定时，若本机已有 Chrome 在跑，新起的进程会把参数交给
  // 既有实例后立刻退出，`--remote-debugging-port` 于是不生效——报错只剩
  // 「chrome did not expose a debugging endpoint」，看不出真正原因。
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'ppts-probe-'));
  const args = [
    '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage', '--mute-audio',
    '--autoplay-policy=no-user-gesture-required',
    `--user-data-dir=${profile}`,
    `--remote-debugging-port=${port}`, 'about:blank'
  ];
  // chrome-headless-shell 本身就是无头，多给 --headless 反而不认；完整版 Chrome 必须给。
  if (!HEADLESS_SHELL) args.unshift('--headless=new');
  const chrome = spawn(CHROME, args, { stdio: ['ignore', 'ignore', 'pipe'] });
  chrome.stderr.on('data', () => {});
  const cleanup = () => {
    try { chrome.kill('SIGKILL'); } catch { /* 已退出 */ }
    try { fs.rmSync(profile, { recursive: true, force: true }); } catch { /* 交给系统 */ }
  };
  cleanupAll = cleanup;

  const get = (p) => new Promise((resolve, reject) => {
    const req = http.get({ host: '127.0.0.1', port, path: p }, (res) => {
      let body = '';
      res.on('data', (c) => (body += c));
      res.on('end', () => { try { resolve(JSON.parse(body)); } catch (e) { reject(e); } });
    });
    req.on('error', reject);
    req.setTimeout(1000, () => req.destroy(new Error('timeout')));
  });

  let version = null;
  for (let i = 0; i < 80 && !version; i++) {
    try { version = await get('/json/version'); } catch { await sleep(150); }
  }
  if (!version) throw new Error('chrome did not expose a debugging endpoint');

  const ws = await openSocket(version.webSocketDebuggerUrl);

  let seq = 0;
  const pending = new Map();
  const events = [];
  ws.onMessage((raw) => {
    const msg = JSON.parse(raw);
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(`${msg.error.message || 'cdp error'} (${msg.error.code})`));
      else resolve(msg.result);
    } else if (msg.method) {
      events.push(msg);
    }
  });
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++seq;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params, sessionId }));
  });

  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const S = (method, params) => send(method, params, sessionId);

  await S('Runtime.enable');
  await S('Log.enable');
  await S('Page.enable');

  const hooks = [HOOK];
  if (mode === 'nowaapi') hooks.push(HOOK_NO_WAAPI);
  if (mode === 'lowmem') hooks.push(HOOK_LOWMEM);
  if (mode !== 'nojs') {
    await S('Page.addScriptToEvaluateOnNewDocument', { source: hooks.join('\n') });
  }
  if (mode === 'nojs') {
    await S('Emulation.setScriptExecutionDisabled', { value: true });
  }
  if (mode === 'reduced') {
    await S('Emulation.setEmulatedMedia', {
      features: [{ name: 'prefers-reduced-motion', value: 'reduce' }]
    });
  }

  // file:// 不能用 path.posix.join 拼：Windows 上会得到 file://C:\...\index.html，
  // Chrome 直接拒收（net::ERR_INVALID_URL）。pathToFileURL 会带上盘符与编码。
  const indexFile = path.resolve(pkgDir, 'index.html');
  if (!fs.existsSync(indexFile)) throw new Error(`包里没有 index.html：${indexFile}`);
  const url = pathToFileURL(indexFile).href;
  await S('Page.navigate', { url });
  // 等首屏与首次 rAF：播放器在 DOMContentLoaded 里建 DOM，动画层在首帧渲染时创建。
  // 固定等待比轮询稳定——轮询会在层还没建出来时读到 0，然后误判成"低内存生效了"。
  await sleep(mode === 'play' ? 1500 : 1200);

  let expr = PLAY_EXPR;
  if (mode === 'nojs') expr = NOJS_EXPR;
  else if (mode === 'default' || mode === 'nowaapi' || mode === 'reduced' || mode === 'lowmem') expr = LAYER_EXPR;

  const evaluated = await S('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (evaluated.exceptionDetails) {
    throw new Error('evaluate failed: ' + (evaluated.exceptionDetails.exception?.description || evaluated.exceptionDetails.text));
  }

  const consoleErrors = events
    .filter((e) => e.method === 'Runtime.consoleAPICalled' && e.params.type === 'error')
    .map((e) => e.params.args.map((a) => a.value ?? a.description ?? '').join(' '));
  const logErrors = events
    .filter((e) => e.method === 'Log.entryAdded' && e.params.entry.level === 'error')
    .map((e) => e.params.entry.text);

  process.stdout.write('PROBE_JSON ' + JSON.stringify({
    mode, url, chrome: CHROME, ok: true,
    result: evaluated.result?.value ?? null,
    consoleErrors, logErrors
  }) + '\n');

  ws.close();
  cleanup();
}

main().then(
  () => process.exit(0),
  (err) => {
    cleanupAll();
    process.stdout.write('PROBE_JSON ' + JSON.stringify({ mode, chrome: CHROME, ok: false, error: String(err && err.message || err) }) + '\n');
    process.exit(1);
  }
);
