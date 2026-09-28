// 分片上传会话（创建 / 上传 / 完成 / 中止）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ClientIdentity } from './identity';
import { connectJSON } from './http';
export type UploadSession = {
  uploadId: string;
  signedUploadUrls: string[];
  chunkSizeBytes: number;
  objectKey: string;
};

export type CompletedUpload = {
  sourceRevisionId: string;
  jobId: string;
  warnings?: string[];
};

export async function createUpload(
  identity: ClientIdentity,
  projectId: string,
  filename: string,
  sizeBytes: number
): Promise<UploadSession> {
  return connectJSON<UploadSession>(identity, '/ppts.v1.UploadService/CreateUpload', {
    projectId,
    filename,
    sizeBytes
  });
}

export async function uploadToURL(
  url: string,
  file: File,
  opts: { onProgress?: (loaded: number, total: number) => void; signal?: AbortSignal } = {}
): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url, true);
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && opts.onProgress) opts.onProgress(event.loaded, event.total);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve();
      else reject(new Error(`upload failed: HTTP ${xhr.status}`));
    };
    xhr.onerror = () => reject(new Error('upload failed: network error'));
    xhr.onabort = () => reject(new DOMException('upload canceled', 'AbortError'));
    if (opts.signal) {
      if (opts.signal.aborted) {
        xhr.abort();
        return;
      }
      opts.signal.addEventListener('abort', () => xhr.abort(), { once: true });
    }
    xhr.send(file);
  });
}

export async function completeUpload(
  identity: ClientIdentity,
  uploadId: string,
  expectedHash: string,
  sizeBytes: number
): Promise<CompletedUpload> {
  return connectJSON<CompletedUpload>(
    identity,
    '/ppts.v1.UploadService/CompleteUpload',
    { uploadId, expectedHash, sizeBytes }
  );
}

export async function abortUpload(identity: ClientIdentity, uploadId: string): Promise<void> {
  await connectJSON<Record<string, never>>(identity, '/ppts.v1.UploadService/AbortUpload', { uploadId });
}
