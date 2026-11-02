#!/usr/bin/env node
// 构建期检查：镜像里的合成器源码能不能被打包。
//
// 为什么需要：`renderer/src` 的三个文件 import 了仓库根的 `../../shared/{motion,shots,types}`，
// 而 Dockerfile 的 COPY 是一张白名单。漏掉 `shared/` 那次，构建成功、CI 全绿（CI 不构建
// 镜像、只跑宿主机上的 render.mjs），镜像一跑就是 webpack 的
// `Can't resolve '/shared/motion'`。自检也能抓到，但要等整层建完、再把 Chrome 开起来。
//
// 这一步只用 webpack，不启动浏览器，几十秒；抓的是「镜像里的文件够不够」，
// 与「渲染出来的像素对不对」是两件事，不要合并。
//
// 用法：node check-bundle.mjs [entry.tsx]（默认 /app/src/index.tsx）

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { bundle } from '@remotion/bundler';

const entryPoint = process.argv[2] ?? '/app/src/index.tsx';
if (!fs.existsSync(entryPoint)) {
  console.error(`entry missing: ${entryPoint}`);
  process.exit(1);
}

// publicDir 必须是真实目录：bundle() 会把它当静态资源根。用临时目录而不是 /app，
// 免得在只读挂载或非 root 场景下撞权限。
const publicDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ppts-bundle-check-'));
try {
  const serveUrl = await bundle({ entryPoint, publicDir, onProgress: () => undefined });
  if (!serveUrl || !fs.existsSync(serveUrl)) {
    console.error(`bundle produced no output at ${serveUrl}`);
    process.exit(1);
  }
  console.log(`bundle ok: ${serveUrl}`);
} catch (err) {
  // webpack 的解析错误信息本来就够定位（它会把试过的每条路径列出来），原样透出。
  console.error(`bundle failed: ${err instanceof Error ? err.message : String(err)}`);
  process.exit(1);
} finally {
  fs.rmSync(publicDir, { recursive: true, force: true });
}
