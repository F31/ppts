// 在线分享观看埋点（设计 §20.8 V20）——纯函数层。
//
// 这一层**不碰网络、不读全局**：所有判定接收一个 TelemetryEnv 快照，副作用留在
// main.ts。理由是这个模块要被门禁在 Node 里直接跑，而「file:// 双击不上报」
// 这类判据一旦只能在真实浏览器里验，就等于没有判据。
//
// 边界（用户已确认：仅在线分享上报）：
//   - 只有 http(s) + /s/<32位token>/ 这个在线形态才启用；
//   - file:// 本地包、门禁静态托管、DNT / GPC / ?privacy=1 一律零请求；
//   - 成品包仍是零外链、断网可播，V14 外部引用数不变。

export const PROGRESS_BUCKETS = [25, 50, 75, 100] as const;
export type ProgressBucket = (typeof PROGRESS_BUCKETS)[number];

/** 服务端认的事件集合：view + 四个进度桶，封闭集合，门禁按字面量核对。 */
export const TELEMETRY_EVENT_NAMES = [
  'view',
  'progress_25',
  'progress_50',
  'progress_75',
  'progress_100'
] as const;

export type ShareViewEvent =
  | { kind: 'view' }
  | { kind: 'progress'; bucket: ProgressBucket };

export type TelemetryEnv = {
  protocol: string;
  pathname: string;
  search: string;
  host: string;
  referrer: string;
  doNotTrack: string | null;
  globalPrivacyControl: boolean;
};

/** 从当前页面读一次环境快照。只在 mount 时调用，之后再不碰全局。 */
export function readTelemetryEnv(): TelemetryEnv {
  const nav = navigator as Navigator & { globalPrivacyControl?: boolean; doNotTrack?: string | null };
  return {
    protocol: location.protocol,
    pathname: location.pathname,
    search: location.search,
    host: location.host,
    referrer: document.referrer || '',
    doNotTrack: nav.doNotTrack ?? null,
    globalPrivacyControl: nav.globalPrivacyControl === true
  };
}

const ONLINE_SHARE_PATH = /^\/s\/[0-9a-f]{32}\/$/;

/**
 * 是否允许上报。判据刻意严格：宁可少报，也不能让自包含成品包在离线/隐私模式下
 * 发出任何一个请求——V16 要求无 404、无 console error，而离线环境里请求必然失败。
 */
export function shouldReportTelemetry(env: TelemetryEnv): boolean {
  if (env.protocol !== 'http:' && env.protocol !== 'https:') return false;
  if (!ONLINE_SHARE_PATH.test(env.pathname)) return false;
  if (env.doNotTrack === '1' || env.globalPrivacyControl) return false;
  try {
    const flag = new URLSearchParams(env.search).get('privacy');
    if (flag !== null && (flag === '1' || flag.toLowerCase() === 'true')) return false;
  } catch {
    return false;
  }
  return true;
}

/** 同源相对端点。不拼绝对 URL、不拼 host：成品包可以被托管在任意子路径下。 */
export function telemetryEndpoint(pathname: string): string | null {
  if (!ONLINE_SHARE_PATH.test(pathname)) return null;
  return `${pathname}events`;
}

const SOURCE_ALLOWED = /^[A-Za-z0-9._-]+$/;
const MAX_SOURCE = 64;

/**
 * 分享来源。只取显式 utm_source/from 或 referrer 的 **host**，绝不把完整 URL
 * （可能带查询串、口令、内部路径）写进埋点。同源 referrer 归为 internal，
 * 无 referrer 归为 direct。
 */
export function shareSource(env: TelemetryEnv): string {
  let explicit = '';
  try {
    const params = new URLSearchParams(env.search);
    explicit = params.get('utm_source') || params.get('from') || '';
  } catch {
    explicit = '';
  }
  if (explicit) return cleanSource(explicit);
  if (!env.referrer) return 'direct';
  try {
    const host = new URL(env.referrer).hostname;
    if (!host) return 'direct';
    if (host.toLowerCase() === env.host.split(':')[0].toLowerCase()) return 'internal';
    return cleanSource(host);
  } catch {
    return 'direct';
  }
}

function cleanSource(raw: string): string {
  const value = raw.trim().slice(0, MAX_SOURCE);
  return SOURCE_ALLOWED.test(value) ? value : '';
}

/**
 * 本次播放位置真实跨过的进度桶。
 *
 * `reported` 同时承担「已上报」与「已跳过」两种语义：拖动进度条时把跳过的桶
 * 标进去（markSkippedBuckets），于是「拖到 80%」不会凭空生成 25/50/75 三条
 * 从未播过的进度。埋点要说实际发生的事，不是把集合凑齐。
 */
export function crossedBuckets(ratio: number, reported: ReadonlySet<number>): ProgressBucket[] {
  if (!Number.isFinite(ratio) || ratio < 0) return [];
  const pct = Math.min(100, ratio * 100);
  return PROGRESS_BUCKETS.filter((bucket) => pct >= bucket && !reported.has(bucket));
}

/** 把「跳到某个位置」之前的所有桶标成不再上报，但不发任何事件。 */
export function markSkippedBuckets(ratio: number, reported: Set<number>): void {
  if (!Number.isFinite(ratio) || ratio < 0) return;
  const pct = Math.min(100, ratio * 100);
  for (const bucket of PROGRESS_BUCKETS) {
    if (pct >= bucket) reported.add(bucket);
  }
}

/** 一次观看的匿名会话 ID：32 位小写十六进制，与任何账号无关。 */
export function newSessionId(): string {
  const bytes = new Uint8Array(16);
  const cryptoObj = globalThis.crypto;
  if (cryptoObj && typeof cryptoObj.getRandomValues === 'function') {
    cryptoObj.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  }
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}
