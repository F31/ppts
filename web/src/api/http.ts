// 全站共享的 HTTP helper 与请求头偏好设置
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { requestJSON } from '../core';
import { ClientIdentity, identityHeaders, languageHeader } from './identity';
export const DOWNLOAD_TIMEOUT_MS = 120_000;

export async function connectJSON<T>(identity: ClientIdentity, procedure: string, body: unknown, extraHeaders?: Record<string, string>): Promise<T> {
  return requestJSON<T>({
    method: 'POST',
    url: procedure,
    headers: { 'Content-Type': 'application/json', ...identityHeaders(identity), ...languageHeader(), ...extraHeaders },
    body,
    label: procedure
  });
}

// getJSON 调用后端原生 HTTP GET 端点（不走 Connect RPC），用于公开区/创作辅助等无法经 proto 生成的接口。
export async function getJSON<T>(identity: ClientIdentity, path: string): Promise<T> {
  return requestJSON<T>({
    method: 'GET',
    url: path,
    headers: { ...identityHeaders(identity), ...languageHeader() },
    label: `GET ${path}`
  });
}

// putJSON 调用后端原生 HTTP PUT 端点（用于来源选择等轻量原生接口）。
export async function putJSON<T>(identity: ClientIdentity, path: string, body: Record<string, unknown>): Promise<T> {
  return requestJSON<T>({
    method: 'PUT',
    url: path,
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body,
    label: `PUT ${path}`
  });
}

// patchJSON 调用后端原生 HTTP PATCH 端点（单字段更新：ppt 展示名、单页备注）。
export async function patchJSON<T>(identity: ClientIdentity, path: string, body: Record<string, unknown>): Promise<T> {
  // 空体（204/空 200）由 requestJSON 统一返回 undefined，成功不会被 JSON.parse 读成失败。
  return requestJSON<T>({
    method: 'PATCH',
    url: path,
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body,
    label: `PATCH ${path}`
  });
}

// postJSON 调用后端原生 HTTP POST 端点（标签/分组创建等）。
export async function postJSON<T>(identity: ClientIdentity, path: string, body: Record<string, unknown>): Promise<T> {
  return requestJSON<T>({
    method: 'POST',
    url: path,
    headers: { ...identityHeaders(identity), ...languageHeader(), 'Content-Type': 'application/json' },
    body,
    label: `POST ${path}`
  });
}

// deleteJSON 调用后端原生 HTTP DELETE 端点（标签/分组删除等）。
export async function deleteJSON<T>(identity: ClientIdentity, path: string): Promise<T> {
  return requestJSON<T>({
    method: 'DELETE',
    url: path,
    headers: { ...identityHeaders(identity) },
    label: `DELETE ${path}`
  });
}
