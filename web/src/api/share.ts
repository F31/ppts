// 分享：匿名播放页取数与链接拼接。
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
//
// 范围说明：项目级「私密分享与协作者」管理端接口（/projects/{pid}/shares 与
// /collaborators）曾在这里有完整封装，其唯一调用方 ShareDialog 在账号分叉
// （c460bf2）后已从路由树摘除，接口随之成为死代码并已删除。匿名侧的 /shared/{token}
// 仍在使用中，因此本文件保留匿名取数与 shareUrl。要恢复管理端，从 git 历史取回
// ShareDialog.tsx 与对应的七个函数即可。

import { requestJSON } from '../core';
import type { PlaybackManifest, SharedMeta } from '../types';

// 匿名端：口令只经请求头传递（不进 URL / 访问日志）。
async function sharedGet<T>(path: string, password?: string): Promise<T> {
  const headers: Record<string, string> = {};
  if (password) headers['X-Share-Password'] = password;
  // 必须走 Connect 契约：SharedWatch 用 isNotFound 决定「链接无效/口令错误」的呈现，
  // 抛原生 Error 会让该判据恒为 false，把这两种正常业务语义显示成"加载失败"。
  // requestJSON 内部统一走 core.ts 的唯一 httpConnectError 实现。
  return requestJSON<T>({ method: 'GET', url: path, headers, label: `GET ${path}` });
}

export function getSharedMeta(token: string): Promise<SharedMeta> {
  return sharedGet<SharedMeta>(`/shared/${encodeURIComponent(token)}`);
}

export function getSharedManifest(token: string, password?: string): Promise<PlaybackManifest> {
  return sharedGet<PlaybackManifest>(`/shared/${encodeURIComponent(token)}/manifest`, password);
}

// shareUrl 把后端给的相对路径拼成绝对地址，供"复制链接"使用。
export function shareUrl(url: string): string {
  if (!url) return '';
  if (/^https?:\/\//i.test(url)) return url;
  return `${window.location.origin}${url.startsWith('/') ? url : `/${url}`}`;
}
