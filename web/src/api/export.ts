// 导出任务与成品下载链接
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ArtifactFormat } from '../types';
import type { ClientIdentity } from './identity';
import { connectJSON } from './http';
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
