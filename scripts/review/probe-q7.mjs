// 评审页自检：真的在浏览器里放一遍，确认评审人能用。
//
// 为什么要有：第 7 项的判据是「段落开口后 3 帧内出现」，靠的是评审页的逐帧能力；
// 页面一旦坏掉（视频没加载、读数不动、导出不拦），回收到的分数就不可解释，
// 而且从分数上看不出来——5.6 那四个 1 分就是这种情况。这个脚本把「页面能评」本身
// 变成可回归的检查。
//
// 用法：
//   node scripts/review/probe-q7.mjs                       # 默认 http://127.0.0.1:8089/
//   PAGE_URL=... node scripts/review/probe-q7.mjs
// 依赖：本机 Chrome；服务先起来（python3 scripts/review/serve.py --root tmp/q7 ...）。
import { spawn } from "node:child_process";

// 端口与 profile 每跑一次换一份：上一轮残留的无头进程会占着端口和 profile，
// 新实例起不来，而错误只表现为「CDP 没起来」，很容易误判成页面坏了。
const PORT = process.env.CDP_PORT || String(9400 + Math.floor(Math.random() * 400));
const PROFILE = `${process.cwd()}/tmp/q7/chrome-prof-${process.pid}`;
const PAGE = process.env.PAGE_URL || "http://127.0.0.1:8089/";
const CHROME = process.env.CHROME || "C:/Program Files/Google/Chrome/Application/chrome.exe";

const child = spawn(CHROME, [
  "--headless=new", "--no-proxy-server", `--remote-debugging-port=${PORT}`,
  `--user-data-dir=${PROFILE}`, "--no-first-run",
  "--autoplay-policy=no-user-gesture-required", "about:blank",
], { stdio: "ignore" });

async function endpoint() {
  for (let i = 0; i < 60; i++) {
    try { return await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json(); }
    catch { await new Promise((r) => setTimeout(r, 250)); }
  }
  throw new Error("CDP 没起来");
}

const { webSocketDebuggerUrl } = await endpoint();
const ws = new WebSocket(webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });

let id = 0;
const pending = new Map();
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
};
function send(method, params = {}, sessionId) {
  const msg = { id: ++id, method, params };
  if (sessionId) msg.sessionId = sessionId;
  ws.send(JSON.stringify(msg));
  return new Promise((res) => pending.set(msg.id, res));
}

const { result: { targetId } } = await send("Target.createTarget", { url: "about:blank" });
const { result: { sessionId } } = await send("Target.attachToTarget", { targetId, flatten: true });
const S = (m, p) => send(m, p, sessionId);
const evalJs = async (expr) => {
  const r = await S("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.result?.exceptionDetails) throw new Error(r.result.exceptionDetails.text + " / " + expr.slice(0, 60));
  return r.result?.result?.value;
};

await S("Page.enable");
await S("Runtime.enable");
await S("Page.navigate", { url: PAGE });
await new Promise((r) => setTimeout(r, 2500));

let bad = 0;
function check(name, ok, detail = "") {
  console.log((ok ? "  ok   " : "  FAIL ") + name + (detail ? "  " + detail : ""));
  if (!ok) bad++;
}

const st = await evalJs(`(() => {
  const v = document.getElementById("film");
  return { ready: v && v.readyState, dur: v && v.duration, muted: v && v.muted,
           vol: v && v.volume, err: v && v.error && v.error.code,
           w: v && v.videoWidth, h: v && v.videoHeight,
           segs: document.querySelectorAll(".seg").length,
           rows: document.querySelectorAll("#sheet tbody tr").length,
           radios: document.querySelectorAll("#sheet input[type=radio]").length };
})()`);
console.log("成片 " + st.w + "×" + st.h + " · " + st.dur.toFixed(2) + "s · " + st.segs + " 段");
check("视频可解码（readyState=4，无 error）", st.ready === 4 && !st.err, "readyState=" + st.ready);
check("音轨未静音", st.muted === false && st.vol > 0);

check("评分表 5 行 × 3 档", st.rows === 5 && st.radios === 15, st.rows + " 行 / " + st.radios + " 个选项");

// 机器门禁卡片：必须存在、必须默认收起（免得数字先把人推向 5 分）、必须有内容
const gateCard = await evalJs(`(() => {
  const g = document.getElementById("gate"), b = document.getElementById("gateBody");
  return { exists: !!g, open: g && g.open, rows: b ? b.querySelectorAll(".gt tr").length : 0,
           text: b ? b.textContent : "" };
})()`);
check("机器门禁卡片默认收起", gateCard.exists && gateCard.open === false);
check("门禁列出逐项结果", gateCard.rows >= 10 && /门禁/.test(gateCard.text), gateCard.rows + " 行");

// 逐帧：点两次「›」应前进 2 帧（67ms），读数要显示「距开口 ±N 帧」
await evalJs(`document.querySelectorAll(".seg")[3].click(), 1`);
await new Promise((r) => setTimeout(r, 600));
await evalJs(`document.getElementById("film").pause(), 1`);
const p0 = await evalJs(`(() => ({ t: document.getElementById("film").currentTime,
  read: document.getElementById("frame").textContent }))()`);
await evalJs(`document.getElementById("f1").click(), document.getElementById("f1").click(), 1`);
await new Promise((r) => setTimeout(r, 400));
const p1 = await evalJs(`(() => ({ t: document.getElementById("film").currentTime,
  read: document.getElementById("frame").textContent }))()`);
const dt = Math.round((p1.t - p0.t) * 1000);
check("逐帧步进（两次 › = 2 帧）", dt >= 40 && dt <= 100, dt + "ms");
check("帧读数含「距开口」", /距开口|开口这一帧/.test(p1.read), p1.read);

// 变速
const rate = await evalJs(`(() => {
  const b = [...document.querySelectorAll("#spd button")].find(x => x.textContent === "0.5×");
  b.click(); return document.getElementById("film").playbackRate;
})()`);
check("变速 0.5×", rate === 0.5);

// 静音提示（volumechange 是异步派发的，要等一拍）
const snd = await evalJs(`(async () => {
  const v = document.getElementById("film"), w = document.getElementById("sndWarn");
  v.muted = true; await new Promise(r => setTimeout(r, 120));
  const on = !w.hidden;
  v.muted = false; await new Promise(r => setTimeout(r, 120));
  return { on, off: w.hidden };
})()`);
check("静音时提示无法评、恢复后消失", snd.on && snd.off);

// 导出前置校验
const gate = await evalJs(`(() => {
  const r = document.querySelectorAll("#sheet input[type=radio]");
  const ev = document.getElementById("export");
  const out = document.getElementById("out");
  for (let i = 0; i < 5; i++) r[i * 3].click();          // 全 5 分（5 条判据）
  ev.click();
  const pass5 = out.textContent.includes("结论：可用");
  for (let i = 0; i < 5; i++) r[i * 3 + 2].click();      // 全 1 分，定位留空
  ev.click();
  const blocked = out.textContent.includes("还不能导出");
  document.getElementById("locate").value = "第 2 页 7.05s，第一条要点比语音晚约 4 帧";
  ev.click();
  const passed = out.textContent.includes("结论：需返工");
  // 还原成空白，别把测试值留在页面上
  document.querySelectorAll("#sheet input[type=radio]").forEach(x => { x.checked = false; });
  document.getElementById("locate").value = "";
  out.textContent = ""; out.hidden = true;
  return { pass5, blocked, passed };
})()`);
check("全 5 分可导出且结论可用", gate.pass5);
check("低于 5 分无定位时拒绝导出", gate.blocked);
check("补了定位后可导出", gate.passed);

ws.close();
child.kill();
// 无头 Chrome 会派生 renderer：只杀父进程留一堆僵尸，下次起不来
try {
  const { execSync } = await import("node:child_process");
  if (process.platform === "win32") execSync(`taskkill /PID ${child.pid} /T /F`, { stdio: "ignore" });
  else process.kill(-child.pid, "SIGKILL");
} catch {}
try {
  const fs = await import("node:fs/promises");
  await fs.rm(PROFILE, { recursive: true, force: true });
} catch {}
console.log(bad ? `\n${bad} 项未通过` : "\n全部通过");
process.exit(bad ? 1 : 0);
