/**
 * core.ts —— 前端 HTTP 唯一出口（P2-C1）。
 *
 * 抽出它的原因不是"整洁"，而是三处真实缺口：
 *
 * 1) **所有请求都没有超时**。`fetch` 默认永不超时：服务端不响应时 Promise 永远 pending，
 *    界面卡在 loading 而没有失败态——这既是"假状态"，也让用户没有任何修复入口（A26）。
 *    这里统一注入 15s 超时；流式/长连接必须显式传 `timeoutMs: 0` 声明"无超时"，
 *    宁要一个显式的 0，也不要一个被漏掉的默认值。
 * 2) **瞬时抖动 = 直接失败**。一次 502/网络闪断就让用户看到报警面板。这里只对**幂等方法**
 *    （GET/HEAD）做退避重试：POST 重试会重复创建资源，是比抖�动更严重的问题。
 * 3) **错误处理有多份实现**。此前每个 helper 都在重复同一套 `fetch → !ok → httpConnectError → 解析`，
 *    任何一份漏掉 `httpConnectError` 就会让 apiError.ts 的 `isNotFound/isUnimplemented`
 *    静默失效（它们判据是 `instanceof ConnectError`）。
 *
 * 依赖方向（无环）：core.ts 不认识 identity/语言偏好等 api 层概念，只接受已构造好的 headers。
 * apiError.ts 现在依赖 core（而不是反过来依赖会发请求的 api.ts）。
 */

export const DEFAULT_TIMEOUT_MS = 15000;

// MAX_ATTEMPTS 是总尝试次数（首次 + 重试）。保持 3 而不是更大：
// 用户此刻就在等这个请求，指数退避下三次已经覆盖绝大多数瞬时故障，再多只是让用户多等。
export const MAX_ATTEMPTS = 3;

export class ConnectError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = 'ConnectError';
    this.code = code;
  }
}

// httpConnectError 是全站唯一的「HTTP 失败 → ConnectError」转换实现。
//
// 为什么必须唯一且必须用 ConnectError：apiError.ts 的 isNotFound / isUnimplemented /
// describeApiError 都以 `error instanceof ConnectError` 为判据（探测容错、功能未启用提示等全靠它）。
// 任何一处自制错误解析、或抛出原生 Error，都会让这条判据静默失效——
// 「正常的 404 业务语义」与「未接线的功能」会被当成真故障上报给用户（违反 A26）。
//
// code 优先级：后端 {code,message} 信封优先，缺省时回退 http_<status>。
// 两个入口：connectErrorFrom 构造（流式/SSE 需要把错误交给回调而非 throw），
// httpConnectError 抛出（绝大多数场景）。两者共用同一份解析逻辑，杜绝第二份实现。
export async function connectErrorFrom(response: Response, label: string): Promise<ConnectError> {
  let code = `http_${response.status}`;
  let message = `${label}: HTTP ${response.status}`;
  try {
    const envelope = (await response.json()) as { code?: string; message?: string };
    if (envelope.code) code = envelope.code;
    if (envelope.message) message = envelope.message;
  } catch {
    // 非 JSON 错误体（如网关 HTML 错误页）：保留 http_<status> 与默认 message。
  }
  return new ConnectError(code, message);
}

export async function httpConnectError(response: Response, label: string): Promise<never> {
  throw await connectErrorFrom(response, label);
}

// ─── 重试判据（纯函数，可单测）───────────────────────────────────────────────

// retryableMethod 只允许幂等方法重试。
//
// POST/PATCH/PUT 不在其中：**这些方法的重试会重复写**。一个真实后果是——
// 服务端已有 Idempotency-Key 的端点不会重复创建，但我们无法假设每个端点都有，
// 与其赌，不如把重试限定在没有副作用的读取上。
export function retryableMethod(method: string): boolean {
  const m = method.toUpperCase();
  return m === 'GET' || m === 'HEAD';
}

// retryableStatus 判定哪些状态码值得重试。
//
// 4xx（除 408/429）不重试：401/403/404 重试多少次结果都一样，只会放大服务端负载并延迟用户看到真实原因。
// 502/503/504 代表网关层瞬时故障，重试大概率成功。
export function retryableStatus(status: number): boolean {
  return status === 408 || status === 429 || status === 502 || status === 503 || status === 504;
}

// backoffMs 退避时长：1s → 2s → 4s，带 ±20% 抖动。
//
// 抖动不是可有可无：整页刷新时所有并发请求会同时失败，若无抖动它们会在完全相同的时刻重试，
// 重现一次自我造成的流量尖峰（惊群），而服务端往往正处在崩溃边缘。
export function backoffMs(attempt: number, random: () => number = Math.random): number {
  const base = Math.min(1000 * 2 ** Math.max(0, attempt), 4000);
  const jitter = 0.8 + random() * 0.4; // [0.8, 1.2)
  return Math.round(base * jitter);
}

// shouldRetryNext 综合判定是否发起下一次尝试。抽出这个函数是为了让规则只有一处、且能被测试直接击中。
export function shouldRetryNext(args: {
  method: string;
  attempt: number; // 已完成次数（首次成功后为 1）
  maxAttempts?: number;
}): boolean {
  const max = args.maxAttempts ?? MAX_ATTEMPTS;
  if (!retryableMethod(args.method)) return false;
  return args.attempt < max;
}

// ─── signal 组合 ─────────────────────────────────────────────────────────────

// combinedSignal 把「调用方取消」与「本次尝试超时」合成一个 signal。
//
// 必须**每次尝试重建**：TimeoutSignal 一旦触发就永久 aborted，若跨尝试复用，
// 重试的请求会在发出的同一瞬间被上一次的超时直接取消——表现为"重试后立刻失败，且错误消失得很快"。
// 返回 null 表示不需要任何 signal（两者都缺）。
function combinedSignal(external?: AbortSignal, timeoutMs?: number): { signal?: AbortSignal; done: () => void } {
  const controller = new AbortController();
  const cleanups: Array<() => void> = [];
  let timer: ReturnType<typeof setTimeout> | undefined;

  if (external) {
    if (external.aborted) controller.abort(external.reason);
    else {
      const onAbort = () => controller.abort(external.reason);
      external.addEventListener('abort', onAbort, { once: true });
      cleanups.push(() => external.removeEventListener('abort', onAbort));
    }
  }
  if (timeoutMs && timeoutMs > 0) {
    timer = setTimeout(() => controller.abort(new DOMException('request timeout', 'TimeoutError')), timeoutMs);
  }
  return {
    signal: controller.signal,
    done: () => {
      if (timer) clearTimeout(timer);
      cleanups.forEach((fn) => fn());
    }
  };
}

// abortLike 判定错误是否为中止/超时（不可重试）。
//
// 用户在 effects 清理里主动取消一个请求是正常的生命周期行为，重试它等于把一个已经开始卸载的组件
// 重新挂回网络上——既浪费，也可能把结果写回已卸载的 setState。
function abortLike(error: unknown): boolean {
  const name = (error as { name?: string } | null)?.name;
  return name === 'AbortError' || name === 'TimeoutError';
}

// ─── 请求出口 ────────────────────────────────────────────────────────────────

export type RequestOptions = {
  method: string;
  url: string;
  headers?: Record<string, string>;
  body?: unknown;
  // signal 由调用方提供用于取消（组件卸载等）。与内部超时信号组合，不能直接透传给 fetch。
  signal?: AbortSignal;
  // timeoutMs 为 0 表示无超时。仅流式/长连接（SSE）可以这么做，且必须在调用处注明理由。
  timeoutMs?: number;
  // maxAttempts 覆盖全局 MAX_ATTEMPTS；传 1 表示显式不重试。
  maxAttempts?: number;
  label?: string;
};

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

function serialiseBody(body: unknown): BodyInit | undefined {
  if (body === undefined || body === null) return undefined;
  if (typeof body === 'string') return body;
  if (body instanceof FormData || body instanceof Blob || body instanceof URLSearchParams) return body;
  return JSON.stringify(body);
}

// requestRaw 发出请求并返回原始 Response（不做状态检查、不读 body）。
//
// 用途：调用方需要自己消费流（上传进度、SSE 逐条事件）。普通 CRUD 请用 requestJSON。
export async function requestRaw(opts: RequestOptions): Promise<Response> {
  const { method, url, headers, body, signal, maxAttempts } = opts;
  const timeoutMs = opts.timeoutMs === undefined ? DEFAULT_TIMEOUT_MS : opts.timeoutMs;
  const attemptCap = maxAttempts ?? (retryableMethod(method) ? MAX_ATTEMPTS : 1);
  let lastError: unknown;
  // httpError 承载「HTTP 状态码层面的最终失败」。必须把它带出循环再抛——
  // 若在 try 内部 throw，会被紧随其后的 catch 当成网络异常再走一遍重试判据，
  // 于是 404/401 这类明确不可重试的响应被打满 maxAttempts 次（实测就是这么错的）。
  let httpError: ConnectError | undefined;

  for (let attempt = 1; attempt <= attemptCap; attempt++) {
    // 调用方已经放弃：立刻停止，不重试。
    if (signal?.aborted) throw signal.reason ?? new DOMException('aborted', 'AbortError');

    const combo = combinedSignal(signal, timeoutMs);
    try {
      const response = await fetch(url, {
        method,
        headers,
        body: serialiseBody(body),
        signal: combo.signal
      });
      // 拿到 Response 就意味着这一跳完成，超时定时器不再需要：流式消费可能持续很久，
      // 若定时器还在，长连接会在 timeoutMs 那一刻被硬生生掐断。
      combo.done();

      if (!response.ok) {
        const err = await connectErrorFrom(response, opts.label ?? `${method} ${url}`);
        if (shouldRetryNext({ method, attempt, maxAttempts: attemptCap }) && retryableStatus(response.status)) {
          lastError = err;
          await sleep(backoffMs(attempt - 1));
          continue;
        }
        httpError = err;
        break;
      }
      return response;
    } catch (error) {
      combo.done();
      lastError = error;
      if (abortLike(error)) throw error;
      // 网络层失败（Failed to fetch）：只有幂等方法重试。
      if (shouldRetryNext({ method, attempt, maxAttempts: attemptCap })) {
        await sleep(backoffMs(attempt - 1));
        continue;
      }
      throw error;
    }
  }
  if (httpError) throw httpError;
  throw lastError;
}

// requestJSON 发出请求并把成功响应解析为 JSON（P2-C1 主入口）。
//
// 空体返回 undefined 而不是抛错：PATCH/DELETE 端点普遍返回 204 或空 200，
// 若照搬 response.json() 会让"成功但没有返回体"被 JSON.parse 读成失败（历史缺陷）。
// 注意这与"该返回 JSON 却返回空"在 HTTP 层无法区分：服务端返回 2xx 即为成功语义，
// 需要区分的端点应自行改用 requestRaw 检查。
export async function requestJSON<T>(opts: RequestOptions): Promise<T> {
  const response = await requestRaw(opts);
  const text = await response.text();
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

// requestVoid 用于不关心响应体的端点（登出、删除等）。成功即 Returns。
export async function requestVoid(opts: RequestOptions): Promise<void> {
  await requestRaw(opts);
}
