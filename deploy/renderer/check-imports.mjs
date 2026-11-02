#!/usr/bin/env node
// 检查入口脚本的相对导入在镜像里闭合。
//
// 为什么需要：render.mjs 里 `import ... from './degrade.mjs'`，而 Dockerfile 的
// COPY 曾漏掉 degrade.mjs。那次构建成功、CI 全绿（CI 不构建镜像），镜像一跑就是
// ERR_MODULE_NOT_FOUND——自检能抓到，但要等镜像层全部建完、跑起 Chrome 才报。
// 这一步把它提前到构建期，代价是几百毫秒。
//
// 为什么不用 `import()` 试：render.mjs 顶层直接执行渲染（末尾 await），
// 导入它等于跑一次真实渲染。所以这里是静态解析。
//
// 只跟相对路径导入：`node:` 与包名交给 node_modules，本脚本不负责。

import fs from 'node:fs';
import path from 'node:path';

const roots = process.argv.slice(2);
if (roots.length === 0) {
  console.error('用法: check-imports.mjs <entry.mjs>...');
  process.exit(2);
}

// 无扩展名的导入（例如 `./src/Root`）按 Node 与打包器的解析顺序试。
const CANDIDATES = ['', '.mjs', '.js', '.ts', '.tsx', '/index.mjs', '/index.js', '/index.ts', '/index.tsx'];

const seen = new Set();
let missing = 0;

function resolveTarget(base) {
  for (const suffix of CANDIDATES) {
    const candidate = base + suffix;
    if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) return candidate;
  }
  return null;
}

function walk(file) {
  if (seen.has(file)) return;
  seen.add(file);

  const source = fs.readFileSync(file, 'utf8');
  // 同时匹配 `from './x'` 与 `import('./x')`；不匹配包名与 node: 内置。
  const pattern = /(?:from\s+|import\s*\(\s*)['"](\.[^'"]+)['"]/g;
  for (const match of source.matchAll(pattern)) {
    const spec = match[1];
    const target = resolveTarget(path.resolve(path.dirname(file), spec));
    if (!target) {
      console.error(`missing import ${spec} (from ${file})`);
      missing += 1;
      continue;
    }
    // 只往下走脚本文件；.tsx 由 bundler 解析，这里不递归（它可能 import CSS/图片）。
    if (/\.(mjs|js)$/.test(target)) walk(target);
  }
}

for (const root of roots) {
  if (!fs.existsSync(root)) {
    console.error(`entry missing: ${root}`);
    missing += 1;
    continue;
  }
  walk(root);
}

if (missing > 0) {
  console.error(`imports broken: ${missing} problem(s), ${seen.size} file(s) walked`);
  process.exit(1);
}
console.log(`imports ok: ${seen.size} file(s) walked`);
