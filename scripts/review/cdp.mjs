// 浏览器复用层：解析 Chrome、拉起、连上 CDP。
//
// 存在的理由只有一个：**候选路径只能有一份**。之前 bundle-probe 的候选表是
// 「镜像路径 + 平台路径」写死在探针里，bundle-gate 又靠 `--print-chrome` 去问它；
// 再加一个需要截图的门禁时，第二条候选表就会出现，两边迟早不一致，
// 表现是「同一个包里一条门禁能跑、另一条找不到浏览器」。
//
// 用法：
//   import { resolveChrome, launchBrowser } from './cdp.mjs';
//   const b = await launchBrowser({ width: 1920, height: 1080 });
//   await b.S('Page.navigate', { url });
//   ...
//   b.close();
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';

export const CHROME_CANDIDATES = [
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

// 显式指定优先：写错了要报错，而不是悄悄退到下一个候选——
// 「我明明设了 PPT_CHROME 却没生效」比「找不到 Chrome」难查得多。
export function resolveChrome() {
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
      close: () => { try { ws.close(); } catch { /* 已关闭 */ } },
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
    close: () => { try { ws.close(); } catch { /* 已关闭 */ } },
  };
}

/**
 * 拉起一个浏览器并连接到它。
 *
 * width/height 决定**视口**（不是窗口）：截图与 MP4 帧要逐像素比较，视口比例
 * 必须与成片一致。--force-device-scale-factor=1 同样是为了这个——Windows 上
 * 系统缩放 125% 会让截图变成 2400×1350，脚本不报错，SSIM 却会掉到 0.3 左右。
 */
export async function launchBrowser({ width = 1920, height = 1080, extraArgs = [] } = {}) {
  const chromePath = resolveChrome();
  const headlessShell = /headless/i.test(path.basename(chromePath));

  // 独立的用户数据目录：不指定时，若本机已有 Chrome 在跑，新起的进程会把参数交给
  // 既有实例后立刻退出，`--remote-debugging-port` 于是不生效——报错只剩
  // 「chrome did not expose a debugging endpoint」，看不出真正原因。
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'ppts-cdp-'));
  const port = 9300 + Math.floor(Math.random() * 400);
  const args = [
    '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage', '--mute-audio',
    '--autoplay-policy=no-user-gesture-required',
    '--hide-scrollbars', '--force-device-scale-factor=1',
    `--window-size=${width},${height}`,
    `--user-data-dir=${profile}`,
    `--remote-debugging-port=${port}`,
    ...extraArgs,
    'about:blank',
  ];
  // chrome-headless-shell 本身就是无头，多给 --headless 反而不认；完整版 Chrome 必须给。
  if (!headlessShell) args.unshift('--headless=new');

  const chrome = spawn(chromePath, args, { stdio: ['ignore', 'ignore', 'pipe'] });
  chrome.stderr.on('data', () => {});

  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    try { chrome.kill('SIGKILL'); } catch { /* 已退出 */ }
    try { fs.rmSync(profile, { recursive: true, force: true }); } catch { /* 交给系统 */ }
  };

  const get = (p) => new Promise((resolve, reject) => {
    const req = http.get({ host: '127.0.0.1', port, path: p }, (res) => {
      let body = '';
      res.on('data', (c) => (body += c));
      res.on('end', () => { try { resolve(JSON.parse(body)); } catch (e) { reject(e); } });
    });
    req.on('error', reject);
    req.setTimeout(1000, () => req.destroy(new Error('timeout')));
  });

  try {
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

    return {
      chromePath,
      headlessShell,
      S,
      events,
      close: () => { ws.close(); close(); },
    };
  } catch (err) {
    close();
    throw err;
  }
}
