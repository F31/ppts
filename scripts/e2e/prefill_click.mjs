// 预填的点击级联调：证明"用户点下去以后看到了什么"。
//
// 这一条补的是 Bash + curl 补不上的缺口。接口返回 202 和一份规则底稿，只能说明
// 服务端守约；它不能说明编辑器真的把底稿先摆上了屏，也不能说明模型结果到了以后
// 是替换、是挂一条"采用模型结果"，还是悄悄丢了。
//
// 判据分三层，从弱到强：
//   1. DOM：点下去 2.5s 内按钮进入"正在按讲稿绑定…"，且复核区已显示"N 条预填绑定"
//      —— 模型要 5s 才回，这时看到的东西只可能来自 202 的规则底稿。
//   2. DOM：最终按钮回到可点状态、复核区不再显示"没有采用模型结果"
//      —— 模型结果被采纳，而不是降级成规则。
//   3. 网络（注入的 fetch 探针）：POST 是 pending，随后 GET 拿到的 spec.prefill.route
//      是 model —— 上屏的确实是模型那份，不是规则底稿恰好长得一样。
//
// 不做的事：不判断动效好不好看。这条只回答"异步通道在界面上走得通"。

import {
  connectCDP,
  goto,
  waitFor,
  buttonStateExpr,
  clickButtonExpr,
  bodyTextExpr
} from "./cdp.mjs";

const args = Object.fromEntries(
  process.argv.slice(2).map((a) => {
    const i = a.indexOf("=");
    return i > 0 ? [a.slice(2, i), a.slice(i + 1)] : [a.replace(/^--/, ""), "true"];
  })
);

const BASE = args.base || "http://127.0.0.1:38081";
const PORT = Number(args.cdp || 9222);
const PROJECT = args.project;
const SLIDE = args.slide;
const SLIDE_INDEX = Number(args["slide-index"] ?? 0);
const MODEL_DELAY_MS = Number(args["model-delay"] || 5000);

if (!PROJECT || !SLIDE) {
  console.error("缺参数：--project=<id> --slide=<slideId>");
  process.exit(2);
}

const log = (msg) => console.log("== " + msg);
const fail = (msg) => {
  console.error("\nFAIL: " + msg);
  process.exit(1);
};

// 页面里的 fetch 探针。注入在导航之前，产品代码起来时它已经在位。
// 只读 clone，不改请求也不改响应——探针一旦能改东西，验的就不是产品本身了。
const PROBE = `(() => {
  const orig = window.fetch;
  window.__prefillLog = [];
  window.fetch = async function (...a) {
    const res = await orig.apply(this, a);
    try {
      const url = typeof a[0] === 'string' ? a[0] : (a[0] && a[0].url) || '';
      if (url.indexOf('animation/prefill') >= 0) {
        res.clone().json().then(b => window.__prefillLog.push({ url, status: res.status, body: b })).catch(() => {});
      }
    } catch (e) {}
    return res;
  };
})()`;

const buttonText = (sel) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  return el ? (el.textContent || '').trim() : '';
})()`;

const cdp = await connectCDP(PORT);

try {
  await cdp.send("Page.enable");
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: PROBE });

  // 编辑器不吃 ?slide= 深链（只有 draft/export/rev 三个），打开后自己选第一页。
  // 所以这里必须先点左边栏里的第 N 页，否则「动效」按钮指向的是别的页——
  // 那样即使断言全绿，验的也不是播了讲稿的那一页。
  const url = `${BASE}/projects/${PROJECT}/editor`;
  log(`打开 ${url}`);
  await goto(cdp, url);

  // 1. 等页面列表出来，点中目标页。
  const rail = await waitFor(
    async () => {
      const n = await cdp.evaluate(`document.querySelectorAll('.slide-rail-v2 button').length`);
      return n > SLIDE_INDEX ? n : 0;
    },
    { timeoutMs: 30000, label: "幻灯片列表" }
  ).catch(async (err) => {
    const text = await cdp.evaluate(bodyTextExpr);
    fail(`没等到页面列表。${err.message}\n页面文本：${String(text).slice(0, 400)}`);
  });
  if (rail <= SLIDE_INDEX) fail(`页面列表只有 ${rail} 页，取不到第 ${SLIDE_INDEX + 1} 页`);

  const picked = await cdp.evaluate(
    `(() => { const b = document.querySelectorAll('.slide-rail-v2 button')[${SLIDE_INDEX}];
              if (!b) return 'notfound'; b.click(); return 'ok'; })()`
  );
  if (picked !== "ok") fail(`点不到第 ${SLIDE_INDEX + 1} 页（${picked}）`);
  log(`选中第 ${SLIDE_INDEX + 1} 页（${SLIDE}）`);

  // 2. 等「动效」按钮变成可用。刚切页时它还指着上一页的空状态，
  //    这里必须等 enabled 而不是看到 disabled 就下结论——那会把"还在加载"误判成"没有真实页"。
  const ready = await waitFor(
    async () => (await cdp.evaluate(buttonStateExpr("动效"))) === "enabled",
    { timeoutMs: 20000, label: "编辑器「动效」按钮可用" }
  ).catch(async (err) => {
    const state = await cdp.evaluate(buttonStateExpr("动效"));
    const text = await cdp.evaluate(bodyTextExpr);
    fail(`「动效」按钮一直不可用（状态 ${state}）：${err.message}\n页面文本：${String(text).slice(0, 400)}`);
  });
  if (!ready) fail("「动效」按钮没有变为可用");

  // 3. 打开动效抽屉，等预填入口出现。
  const opened = await cdp.evaluate(clickButtonExpr("动效"));
  if (opened !== "ok") fail(`点不动「动效」按钮（${opened}）`);
  await waitFor(
    async () => (await cdp.evaluate(buttonText(".animation-prefill-action button"))) !== "",
    { timeoutMs: 10000, label: "预填按钮出现" }
  );

  const label0 = await cdp.evaluate(buttonText(".animation-prefill-action button"));
  log(`预填按钮初始文案：${label0}`);

  const clickedAt = Date.now();
  const click = await cdp.evaluate(clickButtonExpr(label0));
  if (click !== "ok") fail(`点不动预填按钮（${click}）`);

  // 3. 规则底稿必须先上屏。模型 5s 才回，这里只给 2.5s：
  //    能看到"绑定 N 条"就说明它来自 202 那份规则结果，不是等模型等出来的。
  const early = await waitFor(
    async () => {
      const label = await cdp.evaluate(buttonText(".animation-prefill-action button"));
      const summary = await cdp.evaluate(
        `(() => { const el = document.querySelector('.animation-prefill .animation-summary');
                  return el ? (el.textContent || '').trim() : ''; })()`
      );
      return label.includes("正在按讲稿绑定") && /\d+\s*条预填绑定/.test(summary)
        ? { label, summary }
        : "";
    },
    { timeoutMs: 2500, label: "点击后 2.5s 内的「底稿已上屏」状态" }
  ).catch(async (err) => {
    const label = await cdp.evaluate(buttonText(".animation-prefill-action button"));
    const summary = await cdp.evaluate(
      `(() => { const el = document.querySelector('.animation-prefill .animation-summary');
                return el ? (el.textContent || '').trim() : ''; })()`
    );
    fail(
      `底稿没有先上屏：${err.message}\n当前按钮=${JSON.stringify(label)} 复核区=${JSON.stringify(summary)}`
    );
  });
  const earlyMs = Date.now() - clickedAt;
  log(`${earlyMs}ms 时按钮="${early.label}"，复核区="${early.summary}"（模型还要 ${MODEL_DELAY_MS}ms 才回）`);

  // 4. 等模型结果被采纳。超时给足：模型延时 + 一次轮询间隔 + 余量。
  const done = await waitFor(
    async () => {
      const label = await cdp.evaluate(buttonText(".animation-prefill-action button"));
      if (label.includes("正在按讲稿绑定")) return "";
      const text = await cdp.evaluate(bodyTextExpr);
      return { label, text };
    },
    { timeoutMs: MODEL_DELAY_MS + 20000, label: "模型结果到达、按钮恢复" }
  );

  if (String(done.text).includes("没有采用模型结果")) {
    const hint = String(done.text)
      .split("\n")
      .find((l) => l.includes("没有采用模型结果"));
    fail(`模型结果没被采纳，界面降级成了规则结果：${hint}`);
  }

  const finalSummary = await cdp.evaluate(
    `(() => { const el = document.querySelector('.animation-prefill .animation-summary');
              return el ? (el.textContent || '').trim() : ''; })()`
  );
  log(`最终按钮="${done.label}"，复核区="${finalSummary}"`);

  // 5. 网络层证据：POST 走的是异步（pending），随后的 GET 拿到了 route=model。
  const probe = await cdp.evaluate(`JSON.stringify(window.__prefillLog || [])`);
  const entries = JSON.parse(probe || "[]");
  const started = entries.find((e) => e.status === 202 && e.body && e.body.status === "pending");
  if (!started) {
    fail(`没有观察到 202/pending 的预填请求。实际记录：${JSON.stringify(entries.map((e) => [e.status, e.body?.status]))}`);
  }
  const finished = entries.find(
    (e) => e.status === 200 && e.body && e.body.status === "succeeded"
  );
  const route = finished?.body?.spec?.prefill?.route ?? "";
  if (route !== "model") {
    fail(`最终结果的 route=${JSON.stringify(route)}，期望 model（界面看着正常，实际没用上模型）`);
  }
  log(`网络：POST 202/pending → GET 200 route=${route}`);

  // 6. 底稿与模型是两份不同的东西才有比较意义。数字相同不算失败（这一页可能恰好
  //    命中同一批元素），但要打出来：相同却声称"替换过"就是自欺。
  const nEarly = Number((early.summary.match(/(\d+)/) || [])[1] || 0);
  const nFinal = Number((finalSummary.match(/(\d+)/) || [])[1] || 0);
  if (nEarly === nFinal) {
    log(`注意：规则底稿与模型结果都是 ${nFinal} 条，绑定数无法区分两者（route 已单独验证）`);
  } else {
    log(`绑定数 ${nEarly} → ${nFinal}：模型结果确实替换了规则底稿`);
  }

  console.log("\n通过：点击 → 底稿先上屏 → 模型结果替换，全程在真浏览器里发生。");
} finally {
  cdp.close();
}
