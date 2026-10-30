// 导出任务与成品下载链接
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ArtifactFormat } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON, getJSON, postJSON } from './http';
// ---- 导出（ExportService） ----

export async function createExport(
  identity: ClientIdentity,
  params: {
    projectId: string;
    format: ArtifactFormat;
    timelineKey: string;
    pagePngKeys: string[];
    burnSubtitles?: boolean;
    includeNotes?: boolean;
    idempotencyKey: string;
  }
): Promise<{ jobId: string }> {
  return connectJSON<{ jobId: string }>(
    identity,
    '/ppts.v1.ExportService/CreateExport',
    {
      projectId: params.projectId,
      format: params.format,
      timelineKey: params.timelineKey,
      pagePngKeys: params.pagePngKeys,
      burnSubtitles: params.burnSubtitles,
      includeNotes: params.includeNotes
    },
    { 'Idempotency-Key': params.idempotencyKey }
  );
}

export async function createDownload(identity: ClientIdentity, artifactId: string, ttlSeconds = 900): Promise<{ signedUrl: string; expiresAtUnix: number }> {
  return connectJSON<{ signedUrl: string; expiresAtUnix: number }>(identity, '/ppts.v1.ExportService/CreateDownload', {
    artifactId,
    ttlSeconds
  });
}

// ---- 动画导出（导出选项 API） ----
//
// 后端约定（internal/api/export_options.go）：
//   GET /api/projects/{id}/exports/animation-options?timelineKey=...
//
// pages 只列时间轴里真实存在的页，revision=0 表示这一页没有规格（导出时按静态渲染）。
// 前端**原样**提交这份身份，不自己拼：导出是对"用户看到的那一版"的承诺，
// 排队期间规格被改，后端会因 Revision 不符把该页降级并留痕，而不是拿新版渲下去。

export type AnimationExportPage = {
  slideId: string;
  language: string;
  sourceRevisionNo: number;
  revision: number;
};

// AnimationCapability 是租户级能力快照（M4）。
//
// enabled 是「有没有资格」，与 AnimationExportOptions.enabled（「此刻真的能出动画」，含渲染服务可达）
// 是两件事：服务挂了时前者 true、后者 false，正好告诉前端「不是没开通，是服务不可用」。
export type AnimationCapability = {
  enabled: boolean;
  trackEditor: boolean;
  preview: boolean;
  /** deploy / tenant / rollout / default / policy */
  source: string;
  /** 灰度桶 0–99；-1 表示未计算 */
  bucket: number;
  reason?: string;
};

export type AnimationExportOptions = {
  enabled: boolean;
  /** 不可用原因码：animation_not_available / animation_disabled_by_deployment / animation_disabled_by_tenant /
   *  animation_not_in_rollout / animation_policy_unreadable / renderer_not_configured / renderer_unavailable: ... */
  reason?: string;
  capabilities?: AnimationCapability;
  pages?: AnimationExportPage[];
  hasTimeline?: boolean;
};

export type AnimationExportSpec = {
  slideId: string;
  language: string;
  sourceRevisionNo: number;
  revision: number;
};

export async function getAnimationExportOptions(
  identity: ClientIdentity,
  projectId: string,
  timelineKey?: string
): Promise<AnimationExportOptions> {
  const query = timelineKey ? `?timelineKey=${encodeURIComponent(timelineKey)}` : '';
  return getJSON<AnimationExportOptions>(
    identity,
    `/api/projects/${encodeURIComponent(projectId)}/exports/animation-options${query}`
  );
}

// 只把真有规格的页交出去。revision=0 的页放进去会被后端拒绝（必须是正数），
// 因为"这一页没有规格"与"依据了某一版"是两件事。
export function animationSpecsOf(options: AnimationExportOptions | null | undefined): AnimationExportSpec[] {
  if (!options?.enabled) return [];
  return (options.pages ?? [])
    .filter((page) => page.revision > 0)
    .map((page) => ({
      slideId: page.slideId,
      language: page.language,
      sourceRevisionNo: page.sourceRevisionNo,
      revision: page.revision
    }));
}

// ExportJobResult 是导出受理结果。
//
// animationDropped 非空表示「后端接了带动画的请求，但这一单渲不出来，已摘掉动画按静态版导出」
// （见 internal/api/export_bundle.go：部署关掉了动画 / 渲染服务不可达）。
// 它**不是**错误：任务照样入队、照样成功，所以不能走 catch。但也绝不能丢——
// 丢了用户就会以为手里是动效视频，而实际是静态版。调用方必须把它显示出来。
export type ExportJobResult = { jobId: string; animationDropped?: string };

// createArtifactExport 是页面侧唯一的导出入口：由它决定走 Connect 还是原生 REST。
//
// 分工与理由：
//   - mp4：走 REST。动画选项是 proto 里没有的字段，而构建环境没有 buf/protoc，
//     加不了。REST 入口与 Connect 入口共用同一份 ExportSnapshot 与同一个导出处理器，
//     所以不构成第二套导出逻辑。
//   - html_bundle：proto enum 里没有该格式，只能走 REST。
//   - 其余格式（web_project / 字幕 / 音频包 / 配音 PPTX）：走 Connect。
//
// 把这条选择收在一处，而不是在每个页面里写 `format === ... ? rest : connect`：
// 两处各写一遍，某天只改了一处就会出现"同一个按钮在编辑器里带字幕、在项目页里不带"。
export async function createArtifactExport(
  identity: ClientIdentity,
  params: {
    projectId: string;
    format: ArtifactFormat | 'html_bundle';
    timelineKey: string;
    pagePngKeys: string[];
    burnSubtitles?: boolean;
    includeNotes?: boolean;
    animation?: AnimationExportSpec[];
    idempotencyKey: string;
  }
): Promise<ExportJobResult> {
  const specs = params.animation ?? [];
  if (params.format === 'html_bundle' || params.format === 'ARTIFACT_FORMAT_MP4') {
    // 路径必须带 /api：本端点在 internal/api/export_bundle.go 里注册为
    // `POST /api/projects/{projectId}/exports`，与动画规格/选项那一族同前缀。
    // 漏掉前缀时 mux 上是 404（没有 SPA 兜底：SPA 只兜 GET），表现为
    // 「点导出 → 立刻提示 HTTP 404」，而且只在 UI 上出现——脚本验收走的是完整路径，
    // 从 M6-1 到本轮一直没被发现。见 api-contract-check.mjs 的路由表校验。
    return postJSON<ExportJobResult>(identity, `/api/projects/${encodeURIComponent(params.projectId)}/exports`, {
      format: params.format === 'html_bundle' ? 'html_bundle' : 'mp4',
      timelineKey: params.timelineKey,
      pagePngKeys: params.pagePngKeys,
      burnSubtitles: params.format === 'ARTIFACT_FORMAT_MP4' ? params.burnSubtitles : undefined,
      includeNotes: params.includeNotes,
      // 空数组与不传等价（后端归一成 nil，两者得到同一个快照键）。
      animation: specs.length > 0 ? { specs } : undefined,
      idempotencyKey: params.idempotencyKey
    });
  }
  if (specs.length > 0) {
    // 只有 mp4 能出动画。走到这里说明调用方把动画挂到了别的格式上——
    // 静默丢掉会让用户以为"勾了但没生效"，宁可明确失败。
    throw new Error('animation is only available for mp4 export');
  }
  return createExport(identity, {
    projectId: params.projectId,
    format: params.format,
    timelineKey: params.timelineKey,
    pagePngKeys: params.pagePngKeys,
    burnSubtitles: params.burnSubtitles,
    includeNotes: params.includeNotes,
    idempotencyKey: params.idempotencyKey
  });
}
