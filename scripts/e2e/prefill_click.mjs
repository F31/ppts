// 预填的点击级联调：证明"用户点下去以后看到了什么"。
//
// 这一条补的是 Bash + curl 补不上的缺口。接口返回 202 和一份规则底稿，只能说明
// 服务端守约；它不能说明编辑器真的把底稿先摆上了屏，也不能说明模型结果到了以后
// 是替换、是挂一条"采用模型结果"，还是悄悄丢了。
//
// 三个场景，对应异步通道的三条分支：
//
//   A 主路径：点下去 → 底稿先上屏 → 模型结果替换，用户没动过，所以静默替换。
//   B 用户改动过：底稿上屏后拖一下强度，模型结果回来时不能盖掉这份手工调整，
//     而要挂出"采用模型结果"由人决定——这条是设计承诺，此前没有任何测试。
//   C 等不到结果：任务一直 pending（worker 没起、版本不认识这个 kind），
//     预算到点必须放开按钮并说明原因——否则按钮永久禁用，用户只能刷新页面。
//     C 不真等 90s，而是把页面时钟拨快；预算判断本身走的是产品代码。
//
// 判据（A）：DOM 2.5s 内看到"正在按讲稿绑定…"且复核区已有"N 条预填绑定"
//   —— 模型 5s 才回，此时看到的只可能来自 202 的规则底稿；网络探针再确认
//   POST 202/pending、最终 route=model。
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
// 讲稿挂在源修订号 1 上：prefill_live.sh 播种后第一次 Update 就是从 1 分叉出来的。
const sourceRevision = "1";
// 前端等待预算是 90s（web/src/animationPrefill.ts 的 PREFILL_WAIT_BUDGET_MS）。
// 拨 120s 越过它，让预算判断在几秒内真实触发，而不是干等一分半。
const BUDGET_BOOST_MS = 120_000;

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
  // 可拨的时钟：场景 C 用它模拟"等了很久"。不能改预算常量——那是产品代码的一部分，
  // 改了验的就是另一个预算；拨时钟才是在真实实现上走一遍同一条判断。
  const realNow = Date.now;
  window.__clockOffset = 0;
  Date.now = () => realNow() + window.__clockOffset;

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

const SUMMARY_EXPR = `(() => {
  const el = document.querySelector('.animation-prefill .animation-summary');
  return el ? (el.textContent || '').trim() : '';
})()`;

const INTENSITY_EXPR = `(() => {
  const el = document.querySelector('.animation-intensity strong');
  return el ? (el.textContent || '').trim() : '';
})()`;

async function rpc(path, body) {
  const url = `${BASE}${path}`;
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      // 语言头必须显式给。Node 的 fetch 会自动带 `Accept-Language: *`，
      // 而服务端在缺 X-PPTS-Language 时优先取 Accept-Language —— 于是它拿 "*"
      // 去查讲稿，得到 404 "script not found"。curl 与 node:http 不发这个头，
      // 所以同一请求它们是 200：同一个服务端，三个客户端的差别全在这个头。
      "X-PPTS-Language": "zh-CN",
      "X-PPTS-Source-Revision": sourceRevision
    },
    body: JSON.stringify(body)
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${url} -> HTTP ${res.status}: ${text.slice(0, 300)}`);
  return JSON.parse(text);
}

// 让内容指纹变掉，否则会命中上一轮的成功缓存走 200 同步通道，异步窗口根本不出现。
//
// 末尾追加递增的零宽空格：内容逐次唯一、渲染上看不出差别。用「加句号／去句号」
// 两态来回切会撞上以前跑出来的缓存——这一条在写的时候踩过，表现是按钮直接变
// 「重新按讲稿绑定」、没有 pending 阶段。
async function bumpContent(scene) {
  const cur = await rpc("/ppts.v1.ScriptService/Get", { projectId: PROJECT, slideId: SLIDE });
  const segs = cur.segments || [];
  if (!segs.length) fail(`场景 ${scene}：这一页没有讲稿分段，预填会被跳过`);

  const marker = "\u200b";
  const display = segs[0].displayText || "";
  const spoken = segs[0].spokenText || "";
  const k = Math.max(
    display.length - display.replace(/\u200b+$/, "").length,
    spoken.length - spoken.replace(/\u200b+$/, "").length
  );
  segs[0].displayText = display.replace(/\u200b+$/, "") + marker.repeat(k + 1);
  segs[0].spokenText = spoken.replace(/\u200b+$/, "") + marker.repeat(k + 1);

  const out = await rpc("/ppts.v1.ScriptService/Update", {
    projectId: PROJECT,
    slideId: SLIDE,
    expectedRevision: String(cur.revision),
    segments: segs
  });
  if (out.conflict) fail(`场景 ${scene}：讲稿更新冲突`);
  log(`场景 ${scene}：讲稿 revision ${cur.revision} → ${out.revision?.revision}（内容指纹已变）`);
}

// 打开编辑器、选中目标页、拉开动效抽屉，停在"可以点预填"的状态。
async function openEditorAndArm(label) {
  // 编辑器不吃 ?slide= 深链（只有 draft/export/rev 三个），打开后自己选第一页。
  // 所以必须先点左边栏第 N 页，否则「动效」按钮指向的是别的页——
  // 那样即使断言全绿，验的也不是播了讲稿的那一页。
  //
  // 末尾的时间戳只是为了让前后两个场景落到不同的 URL：navigate 到同一个地址可能
  // 被当成同文档导航、页面不重建，探针的请求日志会跨场景累积，断言就会拿 A 的记录
  // 去证 B。参数本身没被任何人读取。
  await goto(cdp, `${BASE}/projects/${PROJECT}/editor?e2e=${Date.now()}`);

  const rail = await waitFor(
    async () => {
      const n = await cdp.evaluate(`document.querySelectorAll('.slide-rail-v2 button').length`);
      return n > SLIDE_INDEX ? n : 0;
    },
    { timeoutMs: 30000, label: `${label}：幻灯片列表` }
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

  // 刚切页时按钮还指着上一页的空状态：必须等 enabled，不能看到 disabled 就下结论，
  // 那会把"还在加载"误判成"这一页没有真实页"。
  const ready = await waitFor(
    async () => (await cdp.evaluate(buttonStateExpr("动效"))) === "enabled",
    { timeoutMs: 20000, label: `${label}：编辑器「动效」按钮可用` }
  ).catch(async (err) => {
    const state = await cdp.evaluate(buttonStateExpr("动效"));
    const text = await cdp.evaluate(bodyTextExpr);
    fail(`「动效」按钮一直不可用（状态 ${state}）：${err.message}\n页面文本：${String(text).slice(0, 400)}`);
  });
  if (!ready) fail("「动效」按钮没有变为可用");

  const opened = await cdp.evaluate(clickButtonExpr("动效"));
  if (opened !== "ok") fail(`点不动「动效」按钮（${opened}）`);
  await waitFor(
    async () => (await cdp.evaluate(buttonText(".animation-prefill-action button"))) !== "",
    { timeoutMs: 10000, label: `${label}：预填按钮出现` }
  );

  const label0 = await cdp.evaluate(buttonText(".animation-prefill-action button"));
  log(`${label}：预填按钮初始文案「${label0}」`);
  return label0;
}

function probeEntries(entries) {
  const started = entries.find((e) => e.status === 202 && e.body && e.body.status === "pending");
  const finished = entries.find((e) => e.status === 200 && e.body && e.body.status === "succeeded");
  return { started, finished };
}

// 点击之后，规则底稿必须在模型回来之前就上屏。
// 2.5s 这个上限是判据的一部分：模型要 5s 才回，此时看到的只可能来自 202 那份。
async function waitDraftOnScreen(label) {
  return waitFor(
    async () => {
      const text = await cdp.evaluate(buttonText(".animation-prefill-action button"));
      const summary = await cdp.evaluate(SUMMARY_EXPR);
      return text.includes("正在按讲稿绑定") && /\d+\s*条预填绑定/.test(summary)
        ? { label: text, summary }
        : "";
    },
    { timeoutMs: 2500, label: `${label}：点击后 2.5s 内的「底稿已上屏」状态` }
  ).catch(async (err) => {
    const text = await cdp.evaluate(buttonText(".animation-prefill-action button"));
    const summary = await cdp.evaluate(SUMMARY_EXPR);
    fail(
      `${label}：底稿没有先上屏。${err.message}\n当前按钮=${JSON.stringify(text)} 复核区=${JSON.stringify(summary)}`
    );
  });
}

const cdp = await connectCDP(PORT);

try {
  await cdp.send("Page.enable");
  await cdp.send("Page.addScriptToEvaluateOnNewDocument", { source: PROBE });

  // ---------- 场景 A：用户没动过，模型结果静默替换 ----------
  log("场景 A：主路径");
  await bumpContent("A");
  const labelA = await openEditorAndArm("A");

  const clickedAt = Date.now();
  const click = await cdp.evaluate(clickButtonExpr(labelA));
  if (click !== "ok") fail(`点不动预填按钮（${click}）`);

  const early = await waitDraftOnScreen("A");
  log(`${Date.now() - clickedAt}ms 时按钮「${early.label}」、复核区「${early.summary}」（模型还要 ${MODEL_DELAY_MS}ms 才回）`);

  const done = await waitFor(
    async () => {
      const label = await cdp.evaluate(buttonText(".animation-prefill-action button"));
      if (label.includes("正在按讲稿绑定")) return "";
      return { label, text: await cdp.evaluate(bodyTextExpr) };
    },
    { timeoutMs: MODEL_DELAY_MS + 20000, label: "A：模型结果到达、按钮恢复" }
  );

  if (String(done.text).includes("没有采用模型结果")) {
    const hint = String(done.text).split("\n").find((l) => l.includes("没有采用模型结果"));
    fail(`A：模型结果没被采纳，界面降级成了规则结果：${hint}`);
  }

  const finalSummary = await cdp.evaluate(SUMMARY_EXPR);
  const entriesA = JSON.parse((await cdp.evaluate(`JSON.stringify(window.__prefillLog || [])`)) || "[]");
  const { started, finished } = probeEntries(entriesA);
  if (!started) {
    fail(`A：没有观察到 202/pending。实际：${JSON.stringify(entriesA.map((e) => [e.status, e.body?.status]))}`);
  }
  const routeA = finished?.body?.spec?.prefill?.route ?? "";
  if (routeA !== "model") {
    fail(`A：最终 route=${JSON.stringify(routeA)}，期望 model（界面看着正常，实际没用上模型）`);
  }
  log(`A 通过：按钮「${done.label}」、复核区「${finalSummary}」、POST 202/pending → GET route=${routeA}`);

  const nEarly = Number((early.summary.match(/(\d+)/) || [])[1] || 0);
  const nFinal = Number((finalSummary.match(/(\d+)/) || [])[1] || 0);
  if (nEarly === nFinal) {
    log(`A 注意：规则底稿与模型结果都是 ${nFinal} 条，条数区分不了两者（route 已单独验证）`);
  } else {
    log(`A：绑定数 ${nEarly} → ${nFinal}，模型结果替换了规则底稿`);
  }

  // ---------- 场景 B：用户已经改过，模型结果不能覆盖 ----------
  log("场景 B：等待期间用户改了强度");
  await bumpContent("B");
  const labelB = await openEditorAndArm("B");

  const clickedB = await cdp.evaluate(clickButtonExpr(labelB));
  if (clickedB !== "ok") fail(`B：点不动预填按钮（${clickedB}）`);

  // 必须等 202 的底稿上屏之后再动滑块：底稿是一次 setDraft，会把手改的内容盖掉。
  const earlyB = await waitDraftOnScreen("B");
  log(`B：底稿已上屏（${earlyB.summary}），现在手工调整强度`);

  const before = await cdp.evaluate(
    `(() => { const el = document.querySelector('.animation-intensity input[type=range]');
              return el ? Number(el.value) : -1; })()`
  );
  if (before < 0) fail("B：找不到强度滑块");
  const target = before < 1.3 ? 1.3 : 1.1;

  const setIntensity = await cdp.evaluate(
    `(() => {
      const el = document.querySelector('.animation-intensity input[type=range]');
      if (!el) return 'notfound';
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      setter.call(el, '${target}');
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return el.value;
    })()`
  );
  if (setIntensity === "notfound") fail("B：找不到强度滑块");
  await waitFor(
    async () => (await cdp.evaluate(INTENSITY_EXPR)).startsWith(target.toFixed(2)),
    { timeoutMs: 3000, label: `B：强度被改成 ${target.toFixed(2)}×` }
  ).catch(async () => {
    fail(`B：拖了滑块但界面强度没有变成 ${target.toFixed(2)}×（读到「${await cdp.evaluate(INTENSITY_EXPR)}」）`);
  });
  log(`B：强度已改为 ${target.toFixed(2)}×（模型 ${MODEL_DELAY_MS}ms 后回来）`);

  // 先等按钮恢复（模型结果已到），然后**立刻**看用户那份调整还在不在。
  // 顺序很重要：先等"采用"区块出现的话，实现若是静默覆盖，这里会一直等不到、
  // 直到超时才报错，而报的是"没挂出采用按钮"——掩盖了真正的问题。
  await waitFor(
    async () => {
      const label = await cdp.evaluate(buttonText(".animation-prefill-action button"));
      return label.includes("正在按讲稿绑定") ? "" : label;
    },
    { timeoutMs: MODEL_DELAY_MS + 20000, label: "B：模型结果到达、按钮恢复" }
  );

  // 关键断言：模型结果到了，但用户那份调整还在。
  const intensityAfterArrival = await cdp.evaluate(INTENSITY_EXPR);
  if (!intensityAfterArrival.startsWith(target.toFixed(2))) {
    fail(
      `B：模型结果静默覆盖了用户调整——强度从 ${target.toFixed(2)}× 变成了 ${intensityAfterArrival}`
    );
  }
  const summaryAfterArrival = await cdp.evaluate(SUMMARY_EXPR);
  if (summaryAfterArrival !== earlyB.summary) {
    fail(
      `B：模型结果已经替换了底稿（复核区 ${earlyB.summary} → ${summaryAfterArrival}），` +
        `却同时挂出「采用」按钮——等于把选择权给了一个已经生效的结果`
    );
  }

  const deferred = await waitFor(
    async () => {
      const text = await cdp.evaluate(
        `(() => { const el = document.querySelector('.animation-prefill-deferred');
                  return el ? (el.textContent || '').trim() : ''; })()`
      );
      return text ? { text } : "";
    },
    { timeoutMs: 5000, label: "B：挂出「采用模型结果」" }
  );
  if (!deferred.text.includes("模型结果已就绪")) {
    fail(`B：deferred 区块的文案不对：${JSON.stringify(deferred.text)}`);
  }
  log(`B：模型结果挂起未覆盖（强度仍是 ${intensityAfterArrival}，复核区仍是「${summaryAfterArrival}」）`);

  // 点了"采用"才该生效。
  const adopt = await cdp.evaluate(clickButtonExpr("采用模型结果"));
  if (adopt !== "ok") fail(`B：点不动「采用模型结果」（${adopt}）`);

  await waitFor(
    async () => {
      const text = await cdp.evaluate(
        `(() => { const el = document.querySelector('.animation-prefill-deferred');
                  return el ? 'present' : 'gone'; })()`
      );
      return text === "gone";
    },
    { timeoutMs: 5000, label: "B：采用后 deferred 区块消失" }
  );

  const intensityAfterAdopt = await cdp.evaluate(INTENSITY_EXPR);
  if (intensityAfterAdopt.startsWith(target.toFixed(2))) {
    fail(`B：点了采用，但强度还是 ${intensityAfterAdopt}——模型结果没有真正生效`);
  }
  const entriesB = JSON.parse((await cdp.evaluate(`JSON.stringify(window.__prefillLog || [])`)) || "[]");
  const probeB = probeEntries(entriesB);
  const routeB = probeB.finished?.body?.spec?.prefill?.route ?? "";
  if (routeB !== "model") fail(`B：最终 route=${JSON.stringify(routeB)}，期望 model`);
  log(`B 通过：采用后强度 ${intensityAfterAdopt}（模型那份），route=${routeB}`);

  // ---------- 场景 C：等不到结果，预算到点必须放开按钮 ----------
  log("场景 C：等待超预算");
  await bumpContent("C");
  const labelC = await openEditorAndArm("C");

  const clickedC = await cdp.evaluate(clickButtonExpr(labelC));
  if (clickedC !== "ok") fail(`C：点不动预填按钮（${clickedC}）`);

  const earlyC = await waitDraftOnScreen("C");
  log(`C：底稿已上屏（${earlyC.summary}），把页面时钟拨快 ${BUDGET_BOOST_MS}ms 模拟等待超预算`);

  // 只拨时钟，不改预算常量：预算判断本身要跑产品的真实代码。
  await cdp.evaluate(`window.__clockOffset += ${BUDGET_BOOST_MS};`);

  // 上限必须**小于模型延时**。否则预算失效时模型 5s 后正常返回，按钮同样会放开，
  // 这条断言就变成了"等模型回来"——它验的东西和 A 场景一样，C 白写。
  // 3s 覆盖一次 1.5s 轮询外加调度抖动，同时稳稳落在模型回来之前。
  const released = await waitFor(
    async () => {
      const label = await cdp.evaluate(buttonText(".animation-prefill-action button"));
      return label.includes("正在按讲稿绑定") ? "" : label;
    },
    { timeoutMs: 3000, label: "C：超预算后按钮放开（模型此时还没回）" }
  );

  // 再钉一层：放开的那一刻，模型结果确实还没到。
  const logC = JSON.parse((await cdp.evaluate(`JSON.stringify(window.__prefillLog || [])`)) || "[]");
  if (logC.some((e) => e.status === 200 && e.body && e.body.status === "succeeded")) {
    fail("C：按钮放开时模型结果已经回来了——这次放开不是预算触发的，不能算通过");
  }

  const textC = await cdp.evaluate(bodyTextExpr);
  if (!textC.includes("模型结果迟迟没有回来")) {
    fail(
      `C：按钮放开了，但界面没说清为什么（应出现「模型结果迟迟没有回来」）。` +
        `页面文本：${String(textC).slice(0, 400)}`
    );
  }
  log(`C 通过：按钮「${released}」，面板已说明「模型结果迟迟没有回来…」`);

  console.log(
    "\n通过：A 底稿先上屏→模型替换；B 用户改过时模型结果不覆盖、由人采用；C 等不到时按钮放开。全程在真浏览器里发生。"
  );
} finally {
  cdp.close();
}
