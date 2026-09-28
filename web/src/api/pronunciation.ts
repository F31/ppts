// 发音词典
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { PronunciationDictionary, PronunciationRule } from '../types';
import type { ClientIdentity } from './identity';
import { gatewayPath } from './gateway';
// ---- 发音词典（/api/pronunciation，需认证，tenant 隔离） ----

export async function listDictionaries(identity: ClientIdentity): Promise<PronunciationDictionary[]> {
  const data = (await gatewayPath(identity, 'GET', '/api/pronunciation')) as { dictionaries?: PronunciationDictionary[] };
  return data.dictionaries ?? [];
}

export async function createDictionary(identity: ClientIdentity, input: { name: string; rules: PronunciationRule[] }): Promise<PronunciationDictionary> {
  const data = (await gatewayPath(identity, 'POST', '/api/pronunciation', input)) as { dictionary?: PronunciationDictionary };
  return data.dictionary!;
}

export async function updateDictionary(identity: ClientIdentity, id: string, input: { name: string; rules: PronunciationRule[] }): Promise<PronunciationDictionary> {
  const data = (await gatewayPath(identity, 'PUT', `/api/pronunciation/${encodeURIComponent(id)}`, input)) as { dictionary?: PronunciationDictionary };
  return data.dictionary!;
}

export async function deleteDictionary(identity: ClientIdentity, id: string): Promise<void> {
  await gatewayPath(identity, 'DELETE', `/api/pronunciation/${encodeURIComponent(id)}`);
}
