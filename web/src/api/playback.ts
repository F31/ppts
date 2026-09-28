// 可播放讲解清单
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { ConnectError, requestJSON } from '../core';
import type { PlaybackManifest } from '../types';
import type { ClientIdentity } from './identity';
import { identityHeaders } from './identity';
export async function getPlaybackManifest(params: {
  identity: ClientIdentity;
  projectId: string;
  timelineKey: string;
  pagePngKeys: string[];
  ttlSeconds: number;
}): Promise<PlaybackManifest> {
  // 必须抛出 ConnectError 而非原生 Error：本端点的失败会流到 ProjectEditor 的
  // reportProbe('narration', …)，而 isNotFound / isUnimplemented 只对 ConnectError 生效；
  // 抛原生 Error 会让"配音尚未生成(404)"被上报成"读取配音失败"。
  // requestJSON 内部的错误处理已经走 core.ts 里那一份唯一实现，这里无需重复判断。
  return requestJSON<PlaybackManifest>({
    method: 'POST',
    url: '/ppts.v1.PlaybackService/GetManifest',
    headers: { 'Content-Type': 'application/json', ...identityHeaders(params.identity) },
    label: 'GetManifest',
    body: {
      projectId: params.projectId,
      timelineKey: params.timelineKey,
      pagePngKeys: params.pagePngKeys,
      ttlSeconds: params.ttlSeconds
    }
  });
}
