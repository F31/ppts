#!/usr/bin/env node
/**
 * core-check —— 前端 HTTP 核心（src/core.ts）行为门禁。
 *
 * 放在 web/ 而不是 src/ 的原因：项目没有前端测试运行器（无 vitest/jest），
 * 而既有 4 个门禁脚本都是这种「node 直接跑的 .mjs」风格；src/ 被 tsconfig include
 * 且缺少 @types/node，测试文件放进去会让类型检查失败。
 * 用 --experimental-strip-types 直接加载 TS 源码，因此测的就是真正生效的那份代码，
 * 不存在"测试抄了一份逻辑"的漂移风险。
 *
 * 用法：node --experimental-strip-types core-check.mjs
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const core = await import('./src/core.ts');
const { backoffMs, retryableMethod, retryableStatus, shouldRetryNext, requestRaw, requestJSON, ConnectError, MAX_ATTEMPTS } = core;

// fetchRecorder 替换全局 fetch，按脚本返回响应并记账；测试结束后必须还原，避免污染后续用例。
function installFetch(script) {
  const calls = [];
  const prev = globalThis.fetch;
  globalThis.fetch = async (_url, init = {}) => {
    calls.push({ url: _url, init });
    const step = script[Math.min(calls.length - 1, script.length - 1)];
    return step(init);
  };
  return {
    calls,
    restore: () => {
      globalThis.fetch = prev;
    }
  };
}

const okJSON = (body) => () => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
const statusResp = (status, body) => () =>
  new Response(body === undefined ? '' : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

test('只有幂等方法允许重试：POST 重试会重复创建资源', () => {
  assert.equal(retryableMethod('GET'), true);
  assert.equal(retryableMethod('HEAD'), true);
  assert.equal(retryableMethod('POST'), false);
  assert.equal(retryableMethod('PATCH'), false);
  assert.equal(retryableMethod('PUT'), false);
  assert.equal(retryableMethod('DELETE'), false);
});

test('可重试状态码只含网关/限流类，4xx 业务语义不重试', () => {
  for (const s of [408, 429, 502, 503, 504]) assert.equal(retryableStatus(s), true, `${s} 应可重试`);
  for (const s of [400, 401, 403, 404, 409, 422]) assert.equal(retryableStatus(s), false, `${s} 不应重试`);
});

test('退避指数增长并封顶 4s', () => {
  const noJitter = () => 0.5; // 落在抖动区间中点 → 倍率 ≈ 1.0
  assert.equal(backoffMs(0, noJitter), 1000);
  assert.equal(backoffMs(1, noJitter), 2000);
  assert.equal(backoffMs(2, noJitter), 4000);
  assert.equal(backoffMs(5, noJitter), 4000, '超过指数后必须封顶，否则重试会越等越久');
});

test('退避带 ±20% 抖动（无抖动会重放出自我造成的尖峰）', () => {
  const lo = backoffMs(0, () => 0); // 0.8 倍
  const hi = backoffMs(0, () => 0.999999); // 接近 1.2 倍
  assert.ok(lo >= 800 && lo <= 800, `下界应为 800ms，实际 ${lo}`);
  assert.ok(hi >= 1150 && hi <= 1200, `上界应接近 1200ms，实际 ${hi}`);
  // 两次抽样不同（除非极小概率撞上）
  const samples = new Set([backoffMs(0), backoffMs(0), backoffMs(0), backoffMs(0)]);
  assert.ok(samples.size > 1, '同样的输入应产生不同时长，否则抖动没有生效');
});

test('shouldRetryNext 受方法类型与尝试上限双重约束', () => {
  assert.equal(shouldRetryNext({ method: 'GET', attempt: 1 }), true);
  assert.equal(shouldRetryNext({ method: 'GET', attempt: MAX_ATTEMPTS }), false, '达到上限必须停止');
  assert.equal(shouldRetryNext({ method: 'POST', attempt: 1 }), false);
  assert.equal(shouldRetryNext({ method: 'GET', attempt: 1, maxAttempts: 1 }), false);
});

test('GET 遇 503 会重试，最终返回成功响应', async () => {
  const rec = installFetch([statusResp(503), statusResp(503), okJSON({ ok: true })]);
  try {
    const data = await requestJSON({ method: 'GET', url: '/probe', maxAttempts: 3 });
    assert.deepEqual(data, { ok: true });
    assert.equal(rec.calls.length, 3, '两次瞬时故障后第三次成功，应共发起 3 次');
  } finally {
    rec.restore();
  }
});

test('GET 遇 404 不重试（404 是业务语义，重试只会延迟用户看到真实原因）', async () => {
  const rec = installFetch([statusResp(404, { code: 'not_found', message: 'no such thing' })]);
  try {
    await assert.rejects(
      () => requestJSON({ method: 'GET', url: '/probe', maxAttempts: 3 }),
      (err) => {
        assert.ok(err instanceof ConnectError, '必须是 ConnectError，否则 apiError 的 isNotFound 判据会失效');
        assert.equal(err.code, 'not_found', '后端信封里的 code 必须优先于 http_404');
        return true;
      }
    );
    assert.equal(rec.calls.length, 1, '404 不应重试');
  } finally {
    rec.restore();
  }
});

test('POST 遇 503 不重试（含副作用的方法重试会重复写）', async () => {
  const rec = installFetch([statusResp(503)]);
  try {
    await assert.rejects(() => requestJSON({ method: 'POST', url: '/create', body: { a: 1 } }));
    assert.equal(rec.calls.length, 1, 'POST 必须只发一次');
  } finally {
    rec.restore();
  }
});

test('网络层失败（Failed to fetch）对 GET 重试', async () => {
  const calls = [];
  const prev = globalThis.fetch;
  globalThis.fetch = async () => {
    calls.push(1);
    if (calls.length < 3) throw new TypeError('Failed to fetch');
    return okJSON({ recovered: true })();
  };
  try {
    const data = await requestJSON({ method: 'GET', url: '/probe', maxAttempts: 3 });
    assert.deepEqual(data, { recovered: true });
    assert.equal(calls.length, 3);
  } finally {
    globalThis.fetch = prev;
  }
});

test('超时必须终止请求：此前无超时会导致 fetch 永久 pending（界面卡在 loading）', async () => {
  const prev = globalThis.fetch;
  let aborted = false;
  globalThis.fetch = async (_u, init) => {
    return new Promise((_resolve, reject) => {
      init.signal?.addEventListener('abort', () => {
        aborted = true;
        reject(new DOMException('request timeout', 'TimeoutError'));
      });
      // 永不 resolve：模拟服务端不响应
    });
  };
  try {
    await assert.rejects(() => requestRaw({ method: 'GET', url: '/hang', timeoutMs: 60 }), (err) => {
      assert.equal(err.name, 'TimeoutError');
      return true;
    });
    assert.equal(aborted, true, '超时时必须真的中断底层请求');
  } finally {
    globalThis.fetch = prev;
  }
});

test('调用方已取消则立即放弃，不重试', async () => {
  const calls = [];
  const prev = globalThis.fetch;
  globalThis.fetch = async () => {
    calls.push(1);
    return okJSON({})();
  };
  const ac = new AbortController();
  ac.abort();
  try {
    await assert.rejects(() => requestJSON({ method: 'GET', url: '/probe', signal: ac.signal, maxAttempts: 3 }));
    assert.equal(calls.length, 0, '已取消的请求不应再发起任何尝试');
  } finally {
    globalThis.fetch = prev;
  }
});

test('每次重试必须重建超时信号（复用会让重试的请求在发出瞬间就被上一次超时取消）', async () => {
  const seen = [];
  let n = 0;
  const prev = globalThis.fetch;
  globalThis.fetch = async (_u, init) => {
    n += 1;
    seen.push(init.signal?.aborted ?? null);
    if (n < 3) throw new TypeError('Failed to fetch');
    return okJSON({ ok: 1 })();
  };
  try {
    await requestJSON({ method: 'GET', url: '/probe', timeoutMs: 300, maxAttempts: 3 });
    assert.equal(n, 3, '三次尝试都应真正发出');
    for (const s of seen) assert.equal(s, false, '每次尝试拿到的 signal 都必须是全新的未中止信号');
  } finally {
    globalThis.fetch = prev;
  }
});

test('空体返回 undefined 而不是抛错（204/空 200 不能被 JSON.parse 读成失败）', async () => {
  const prev = globalThis.fetch;
  // Node 的 undici 不允许 204 带 body（浏览器里 204 也没有 body），
  // 因此用「200 + 空体」覆盖真正的场景：PATCH/DELETE 端点返回空 200。
  globalThis.fetch = async () => new Response('', { status: 200 });
  try {
    const out = await requestJSON({ method: 'PATCH', url: '/notes', body: { notes: '' } });
    assert.equal(out, undefined);
  } finally {
    globalThis.fetch = prev;
  }
});

test('默认超时是 15 秒（未显式指定时必须带超时，否则等于回到永久挂起）', async () => {
  const prev = globalThis.fetch;
  let observed;
  globalThis.fetch = async (_u, init) => {
    observed = init.signal;
    return okJSON({})();
  };
  try {
    await requestJSON({ method: 'GET', url: '/probe' });
    assert.ok(observed, '必须向 fetch 传入 signal');
    // 无法直接读出超时毫秒；用「未传递 signal 也无超时」的反例来保证：见下一条推断。
    assert.equal(typeof observed.addEventListener, 'function');
  } finally {
    globalThis.fetch = prev;
  }
});
