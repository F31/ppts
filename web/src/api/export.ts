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

// createHTMLBundleExport 请求「静态讲解成品包」（html_bundle，M6-1）。
//
// 走原生 REST 而不是 CreateExport：新增格式意味着在 proto enum 里加值并重新生成，
// 而构建环境没有 buf/protoc。两个入口产生同一结构的 ExportSnapshot，由同一个导出
// 处理器消费，因此不构成两套导出逻辑。
export async function createHTMLBundleExport(
  identity: ClientIdentity,
  params: {
    projectId: string;
    timelineKey: string;
    pagePngKeys: string[];
    idempotencyKey: string;
  }
): Promise<{ jobId: string }> {
  return postJSON<{ jobId: string }>(identity, `/projects/${encodeURIComponent(params.projectId)}/exports`, {
    format: 'html_bundle',
    timelineKey: params.timelineKey,
    pagePngKeys: params.pagePngKeys,
    idempotencyKey: params.idempotencyKey
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

export type AnimationExportOptions = {
  enabled: boolean;
  /** 不可用原因码：animation_not_available / animation_disabled_by_deployment / renderer_not_configured / renderer_unavailable: ... */
  reason?: string;
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
): Promise<{ jobId: string }> {
  const specs = params.animation ?? [];
  if (params.format === 'html_bundle' || params.format === 'ARTIFACT_FORMAT_MP4') {
    return postJSON<{ jobId: string }>(identity, `/projects/${encodeURIComponent(params.projectId)}/exports`, {
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
