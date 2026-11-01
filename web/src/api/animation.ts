// 逐页动画规格的读写（tasks 3.1–3.3）。
//
// 后端是 REST（internal/api/animation.go），不是 ConnectRPC：
// 一页一份规格是资源式 CRUD，与 /api/context-rules 同形；业务 RPC 面仍走 Connect。
// 三个端点的语义差别很重要，别在这里抹平：
//   GET .../animation            200=有规格 / 404=从没配过（不是错误）
//   GET .../animation/anchors    总能回答"这一页有什么可以动"（与规格无关）
//   POST .../animation/preview   只渲这一页，≤3s，超时明确报错

import { ConnectError, requestRaw } from '../core';
import type { AnimationAnchors, AnimationSavePayload, AnimationSpec } from '../animationState';
import { defaultSpecQuery } from '../animationPrefill';
import { getJSON, postJSON, putJSON } from './http';
import { identityHeaders, languageHeader, type ClientIdentity } from './identity';

export type AnimationScope = {
  projectId: string;
  slideId: string;
  /** 0/undefined = 当前源版本（后端按项目当前 revision 解析）。 */
  sourceRevisionNo?: number;
  language?: string;
};

function basePath(scope: AnimationScope): string {
  return `/api/projects/${encodeURIComponent(scope.projectId)}/slides/${encodeURIComponent(scope.slideId)}/animation`;
}

function queryOf(scope: AnimationScope, extra?: Record<string, string>): string {
  const params = new URLSearchParams();
  if (scope.sourceRevisionNo && scope.sourceRevisionNo > 0) params.set('sourceRevisionNo', String(scope.sourceRevisionNo));
  if (scope.language) params.set('language', scope.language);
  for (const [key, value] of Object.entries(extra ?? {})) params.set(key, value);
  const qs = params.toString();
  return qs ? `?${qs}` : '';
}

// getAnimationAnchors 取页级上下文：可挂元素（含文字）、分段序号、页时长、可选预设。
//
// 它与规格的读取分开，是因为"还没配置过动效的页"最需要它——把两件事塞进一个响应，
// 就得给 404 加语义。
export async function getAnimationAnchors(identity: ClientIdentity, scope: AnimationScope): Promise<AnimationAnchors> {
  return getJSON<AnimationAnchors>(identity, `${basePath(scope)}/anchors${queryOf(scope)}`);
}

// getAnimationSpec 读取已保存的规格；这一页从没配过时返回 null。
//
// 不区分"404 因为没配"与"404 因为页不存在"：两者在面板上的下一步都是"先选一个预设"，
// 而真正的"页不存在"会在 anchors 端点先失败。
export async function getAnimationSpec(identity: ClientIdentity, scope: AnimationScope): Promise<AnimationSpec | null> {
  try {
    return await getJSON<AnimationSpec>(identity, `${basePath(scope)}${queryOf(scope)}`);
  } catch (err) {
    if (err instanceof ConnectError && (err.code === 'not_found' || err.code === 'http_404')) return null;
    throw err;
  }
}

// getDefaultAnimationSpec 让服务端按预设生成一份默认规格（未保存）。
//
// 生成放在服务端而不是前端：预设定义（catalog.json）是唯一来源，前端再实现一遍
// "标题用哪个动效、要点怎么隔段"就等于有了两个版本，改 catalog 时必然漏一处。
//
// prefill=true 时服务端还会跑一遍讲稿绑定预填（规则名 + 置信度写进每条 clip，
// 响应里多一个 prefill 说明）。两个开关都不带时，响应与引入预填之前逐字节相同。
export async function getDefaultAnimationSpec(
  identity: ClientIdentity,
  scope: AnimationScope,
  presetId: string,
  prefill = false
): Promise<AnimationSpec> {
  return getJSON<AnimationSpec>(identity, `${basePath(scope)}${queryOf(scope, defaultSpecQuery(presetId, prefill))}`);
}

/**
 * 预填的异步状态。四个取值各自对应界面要做的下一件事：
 *
 *   succeeded  有结果了，按 spec 渲染
 *   pending    先用响应里带的那份规则规格，继续轮询同一个 jobId
 *   stale      页面内容变了，这次结果对不上当前页面——重新发起一次
 *   failed / canceled  模型没成功，手里的规则规格就是最终结果
 */
export type AnimationPrefillStatus = 'succeeded' | 'pending' | 'stale' | 'failed' | 'canceled';

export type AnimationPrefillResponse = {
  status: AnimationPrefillStatus;
  jobId?: string;
  /** 失败/过期的原因（任务的错误码或 result_missing 等），仅用于提示，不参与判断。 */
  reason?: string;
  spec?: AnimationSpec;
};

/**
 * startAnimationPrefill 发起一次预填。
 *
 * 为什么是 POST 而不是继续用 `GET ?prefill=true`：只有 script 档会问模型，
 * 而那一步要十几秒。同步请求会让「打开编辑器」变成一个可能挂住的长请求，
 * 超时上限还得跟着调——而用户想要的只是「先给我一版」。
 *
 * 服务端的契约是：**总会返回一份可用的 spec**（规则算的）。pending 时这份 spec 是底稿，
 * 模型结果好了再由 getAnimationPrefillResult 换上去。所以这里拿到的 spec 永远可以直接渲染。
 */
export async function startAnimationPrefill(
  identity: ClientIdentity,
  scope: AnimationScope,
  presetId: string
): Promise<AnimationPrefillResponse> {
  const query = presetId ? { preset: presetId } : undefined;
  return postJSON<AnimationPrefillResponse>(identity, `${basePath(scope)}/prefill${queryOf(scope, query)}`, {});
}

/** getAnimationPrefillResult 按 jobId 取异步预填的结果（pending 时返回 status=pending）。 */
export async function getAnimationPrefillResult(
  identity: ClientIdentity,
  scope: AnimationScope,
  jobId: string
): Promise<AnimationPrefillResponse> {
  return getJSON<AnimationPrefillResponse>(identity, `${basePath(scope)}/prefill${queryOf(scope, { jobId })}`);
}

// AnimationConflictError 是 409 的语义化版本：本地草案已经过期。
//
// 带 latest 是因为"冲突了"本身不是可行动的信息——用户需要看到服务器那份是什么样，
// 才能决定是覆盖还是采用。
export class AnimationConflictError extends Error {
  readonly latest: AnimationSpec | null;

  constructor(latest: AnimationSpec | null) {
    super('animation: spec conflict');
    this.name = 'AnimationConflictError';
    this.latest = latest;
  }
}

// saveAnimationSpec 保存草案。expectedRevision 就是草案里的 revision（乐观并发）。
export async function saveAnimationSpec(
  identity: ClientIdentity,
  scope: AnimationScope,
  payload: AnimationSavePayload
): Promise<AnimationSpec> {
  try {
    return await putJSON<AnimationSpec>(identity, `${basePath(scope)}${queryOf(scope)}`, payload as unknown as Record<string, unknown>);
  } catch (err) {
    if (err instanceof ConnectError && err.code === 'conflict') {
      // 409 的响应体里有 latest，但 requestRaw 对非 2xx 直接抛错，读不到 body。
      // 与其为了一个罕见路径另开一条请求通道，不如拉一次最新版——多一次 GET，
      // 换的是"冲突提示里一定能看到服务器那份"。
      let latest: AnimationSpec | null = null;
      try {
        latest = await getAnimationSpec(identity, scope);
      } catch {
        latest = null;
      }
      throw new AnimationConflictError(latest);
    }
    throw err;
  }
}

// previewAnimation 渲染这一页，返回可直接放进 <video> 的 blob URL。
//
// 为什么要 revoke：一次编辑会话里预览十次就有十个 blob，不释放会一直占着内存。
// 调用方负责在替换/卸载时 revokeObjectURL（组件里统一处理）。
export type AnimationPreview = {
  url: string;
  /** 编译出的元素图层数：>0 表示这次预览含元素级动效（导出侧目前还渲不出来）。 */
  layers: number;
};

export async function previewAnimation(
  identity: ClientIdentity,
  scope: AnimationScope,
  timelineKey: string
): Promise<AnimationPreview> {
  // 服务端预算默认 3s、上限 60s（PPTS_ANIMATION_PREVIEW_BUDGET），客户端兜底给到 90s：
  // 永远让服务端的 504 先到达，用户看到的是"这一页渲得太慢"（可重试），
  // 而不是本地超时（看起来像网络断了）。两个数必须保持这个先后关系。
  const res = await requestRaw({
    method: 'POST',
    url: `${basePath(scope)}/preview`,
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body: { timelineKey },
    label: 'POST animation preview',
    timeoutMs: 90000
  });
  const blob = await res.blob();
  const layers = Number(res.headers.get('X-Preview-Layers') ?? '0');
  return { url: URL.createObjectURL(blob), layers: Number.isFinite(layers) ? layers : 0 };
}
