// 导出任务与成品下载链接
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ArtifactFormat } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON, postJSON } from './http';
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
