// 极简 CDP 客户端：只够"打开页面、点按钮、读文本"。
//
// 为什么不用 Playwright：这里要验的是产品自己的代码（fetch、轮询、React 状态），
// 不是选择器库的能力。装一个带浏览器的依赖只为点两下，成本远大于这份 90 行的驱动。
// Node 22 自带 WebSocket，因此零依赖。
//
// 它驱动的是真浏览器：真实的内嵌 dist、真实的 fetch、真实的 React 渲染。
// Bash 脚本里的 curl 只能证明接口通，证明不了"用户点下去以后看到了什么"。

export async function connectCDP(port = 9222, targetMatch = "127.0.0.1") {
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const page =
    list.find((t) => t.type === "page" && (t.url || "").includes(targetMatch)) ||
    list.find((t) => t.type === "page");
  if (!page) throw new Error("CDP 里没有可用的 page target");

  const ws = new WebSocket(page.webSocketDebuggerUrl);
  const pending = new Map();
  let seq = 0;

  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", (e) => reject(new Error("CDP WebSocket 连接失败: " + e.message)), {
      once: true
    });
  });

  ws.addEventListener("message", (event) => {
    let msg;
    try {
      msg = JSON.parse(event.data);
    } catch {
      return;
    }
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(`${msg.error.message} (${msg.error.code})`));
      else resolve(msg.result);
    }
  });

  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = ++seq;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify({ id, method, params }));
    });

  const evaluate = async (expression) => {
    const res = await send("Runtime.evaluate", {
      expression,
      returnByValue: true,
      awaitPromise: true,
      userGesture: true
    });
    if (res.exceptionDetails) {
      const d = res.exceptionDetails;
      throw new Error("页面内求值抛错: " + (d.exception?.description || d.text));
    }
    return res.result?.value;
  };

  const close = () => {
    try {
      ws.close();
    } catch {
      /* 已断开 */
    }
  };

  return { send, evaluate, close };
}

export async function goto(cdp, url) {
  await cdp.send("Page.enable");
  await cdp.send("Page.navigate", { url });
}

/** 轮询直到 fn() 返回真值；超时抛错并带上最后一次的观察值。 */
export async function waitFor(fn, { timeoutMs = 15000, intervalMs = 150, label = "条件" } = {}) {
  const started = Date.now();
  let last;
  while (Date.now() - started < timeoutMs) {
    last = await fn();
    if (last) return last;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(`${label} 在 ${timeoutMs}ms 内没有成立（最后观察值：${JSON.stringify(last)}）`);
}

/** 按文本精确匹配一个按钮的状态，不点击。返回 enabled / disabled / notfound。 */
export function buttonStateExpr(text) {
  return `(() => {
    const want = ${JSON.stringify(text)};
    const el = [...document.querySelectorAll('button')].find(b => (b.textContent || '').trim() === want);
    if (!el) return 'notfound';
    return el.disabled ? 'disabled' : 'enabled';
  })()`;
}

/** 按文本精确匹配点一个按钮，返回 notfound / disabled / ok。 */
export function clickButtonExpr(text) {
  return `(() => {
    const want = ${JSON.stringify(text)};
    const el = [...document.querySelectorAll('button')].find(b => (b.textContent || '').trim() === want);
    if (!el) return 'notfound';
    if (el.disabled) return 'disabled';
    el.click();
    return 'ok';
  })()`;
}

/** 把一段纯文本在 DOM 里找出来，返回包含它的元素文本；找不到返回 ''。 */
export const bodyTextExpr = `document.body ? document.body.innerText : ''`;
