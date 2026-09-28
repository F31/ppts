#!/usr/bin/env node
/**
 * api-contract-check —— 前端 HTTP 契约门禁。
 *
 * 守护三条不变量（都源于真实缺陷）：
 *
 * 1) 网络请求只允许出现在 api/*.ts / auth.ts / core.ts。散落在页面组件里的 fetch 会各自发明错误处理，
 *    而 apiError.ts 的 isNotFound / isUnimplemented / describeApiError 依赖 instanceof ConnectError——
 *    一处自制 Error 就会让探测容错、功能未启用提示静默失效（A26）。
 * 2) 每个发起 fetch 的函数，必须把失败交给 httpConnectError / connectErrorFrom（唯一解析实现），
 *    或显式标注 `// fetch-ok: <理由>`（限外部端点：OIDC IdP、对象存储签名 URL 等）。
 * 3) api.ts 里同一个函数出现 fetch 又出现 `throw new Error(` —— 说明又写了一份自制错误，直接拦。
 *    （auth.ts 不在此列：它对接外部 OIDC IdP，响应不是本项目的 {code,message} 契约，
 *     但仍必须逐处标注 `// fetch-ok: 理由`。）
 *
 * 用法：node api-contract-check.mjs
 */
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const ROOT = process.cwd();
const SRC = join(ROOT, 'src');
// ALLOWED_FILES 是「允许发起网络请求的文件」（相对 src 的路径）。
//
// P2-C1 起加入 core.ts：它是全站唯一的请求出口（超时/重试/错误归一），
// api.ts 的 helper 已经不再自己调 fetch。若不允许它，门禁会把收口本身判成违规。
// P2-C2 起改为路径集合：api.ts 已按域拆到 api/<domain>.ts，按 basename 判定会放过同名文件。
const ALLOWED_FILES = new Set(['api.ts', 'auth.ts', 'core.ts']);

// apiDirFiles 列出 src/api/ 下的所有 .ts（P2-C2 拆分后的域文件）。
// 采用递归扫描而非硬编码清单：新增域文件时无需记得同步这里，
// 清单过期正是这类门禁最常见的失效方式。
function apiDirFiles() {
  const dir = join(SRC, 'api');
  const out = [];
  try {
    for (const name of readdirSync(dir)) {
      if (/\.ts$/.test(name)) out.push(`api/${name}`);
    }
  } catch {
    // 目录不存在（尚未拆分的分支）：留空即可，后续检查按实际文件走。
  }
  return out;
}
const ALLOWED_API_PATHS = new Set(apiDirFiles());

function walk(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) out.push(...walk(p));
    else if (/\.(ts|tsx)$/.test(name)) out.push(p);
  }
  return out;
}

// 顶层/缩进两格以内的函数声明起始行（api.ts / auth.ts 的导出函数均写在第 0 列）。
const FN_START = /^(?:export\s+)?(?:async\s+)?function\s+\w+|^  (?:export\s+)?(?:async\s+)?(?:const|let|function)\s+\w+.*=>\s*\{?\s*$/;

function splitFunctions(lines) {
  // 返回 [{ start, end, name }]
  const fns = [];
  lines.forEach((line, i) => {
    if (FN_START.test(line)) {
      const m = line.match(/function\s+(\w+)/) || line.match(/(?:const|let)\s+(\w+)/);
      fns.push({ start: i, name: m ? m[1] : `<anonymous:${i + 1}>` });
    }
  });
  fns.forEach((fn, idx) => {
    fn.end = idx + 1 < fns.length ? fns[idx + 1].start : lines.length;
  });
  return fns;
}

const errors = [];
const files = walk(SRC);

for (const file of files) {
  const rel = relative(SRC, file).split('\\').join('/');
  const lines = readFileSync(file, 'utf8').split(/\r?\n/);
  const base = rel.split('/').pop();

  const fetchLines = [];
  lines.forEach((l, i) => {
    if (/\bfetch\s*\(/.test(l)) fetchLines.push(i);
  });
  if (fetchLines.length === 0) continue;

  // 不变量 1：只允许在请求层（core.ts / api.ts / api/<domain>.ts / auth.ts）发起请求
  const isApiLayer = ALLOWED_API_PATHS.has(rel) || ALLOWED_FILES.has(rel);
  if (!isApiLayer) {
    errors.push(`${rel}: 不允许在组件/页面里直接调用 fetch（第 ${fetchLines.map((i) => i + 1).join(', ')} 行）。所有网络请求必须走 core.ts/api.ts，以保证错误处理契约一致（参见 httpConnectError）。`);
    continue;
  }

  const fns = splitFunctions(lines);
  for (const lineNo of fetchLines) {
    const fn = fns.filter((f) => lineNo >= f.start && lineNo < f.end).pop();
    if (!fn) {
      errors.push(`${rel}:${lineNo + 1} 的 fetch 不在任何函数体内，无法判定错误处理契约。`);
      continue;
    }
    const body = lines.slice(fn.start, fn.end);
    const bodyText = body.join('\n');
    const delegated = /httpConnectError\s*\(|connectErrorFrom\s*\(/.test(bodyText);
    const marked = /\/\/\s*fetch-ok:/.test(bodyText);
    if (!delegated && !marked) {
      errors.push(`${rel}:${fn.start + 1} 的 ${fn.name}() 发起了 fetch 但既未把失败交给 httpConnectError/connectErrorFrom，也没有 \`// fetch-ok: 理由\` 标注。`);
    }
    // 不变量 3：同一函数里 fetch + throw new Error = 自制错误实现。
    // 约束 core.ts / api.ts / api/<domain>.ts（对接自家后端，错误契约必须唯一）；
    // auth.ts 是与外部 IdP 通信的豁免项。
    if (base !== 'auth.ts' && /throw\s+new\s+Error\s*\(/.test(bodyText)) {
      errors.push(`${rel}:${fn.start + 1} 的 ${fn.name}() 同时出现 fetch 与 throw new Error——自制 HTTP 错误会让 isNotFound/isUnimplemented 静默失效，请改用 httpConnectError。`);
    }
  }
}

// 不变量 2b：信封解析实现只允许一份（防止有人复制出新分支）。
// P2-C2：原来只硬读 src/api.ts，拆分后该文件里的 helper 已迁走，
// 若继续只扫它就变成了永远绿的无效门禁 —— 改为扫描请求层的所有文件。
{
  let total = 0;
  const where = [];
  for (const target of ['core.ts', 'api.ts', ...ALLOWED_API_PATHS]) {
    const full = join(SRC, target.split('/').join('/'));
    let text = '';
    try {
      text = readFileSync(full, 'utf8');
    } catch {
      continue;
    }
    const hits = text.match(/\(\s*await\s+\w+\.json\(\)\s*\)\s*as\s*\{\s*code\?:/g) || [];
    if (hits.length > 0) {
      total += hits.length;
      where.push(`${target}(${hits.length})`);
    }
  }
  if (total > 1) {
    errors.push(`请求层发现 ${total} 份 {code,message} 信封解析实现 [${where.join(', ')}]，应只剩 core.ts connectErrorFrom 里的一份。`);
  }
}

if (errors.length > 0) {
  console.error(`FAIL：API 契约检查未通过（${errors.length} 项）\n`);
  for (const e of errors) console.error(' - ' + e);
  process.exit(1);
}
console.log('OK：前端 HTTP 契约检查通过（请求集中在 api/*.ts/auth.ts/core.ts，错误处理统一走 Connect 契约）。');
