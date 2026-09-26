// Phase 1.5 路由回收清单门禁：确保「后端原生 HTTP 路由」全部登记了分组与处置。
//
// 为什么需要（编译器管不到）：
//   `mux.Handle("GET /xxx", ...)` 里的路径是普通字符串，路由存在与否、归哪个业务域、
//   该留还是该迁，**全都不在类型系统里**。于是清单如果手写，必然腐烂：架构审视 2026-09-25
//   记的是「96 条」，实测已是 **108 条**（server.go 一处就从 11 变 19），两周漂移 12 条。
//
// 设计取向（沿用 i18n-check 的同一套路）：
//   - **逐条清单不入库**：路由现场从 internal/api/*.go 提取，天生与代码同步、不会腐烂。
//   - **只登记机器判不了的**：分组的处置（keep / keep-both / migrate）与理由、
//     以及「同一能力是否双写」的分诊结论——机器判不出「前端到底在用哪一侧」。
//   - **新增未登记 = 失败**：逼人在加路由时就回答「这属于哪一组、留还是迁」。
//
// 用法：
//   node route-inventory-check.mjs              校验（退出码非 0 = 有未登记路由/分诊失真）
//   node route-inventory-check.mjs --report     打印当前全量清单（分组 → 路由）
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dir = dirname(fileURLToPath(import.meta.url));
const API_DIR = join(__dir, '..', 'internal', 'api');
const INV_PATH = join(__dir, 'route-inventory.json');
const REPORT = process.argv.includes('--report');

const METHODS = /^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)$/;

/* ---------- 1. 从 Go 源码现场提取路由 ---------- */
function extractRoutes() {
  const routes = [];
  const connect = [];
  for (const name of readdirSync(API_DIR)) {
    if (!name.endsWith('.go') || name.endsWith('_test.go')) continue;
    const text = readFileSync(join(API_DIR, name), 'utf8');
    text.split('\n').forEach((line, i) => {
      const m = /mux\.Handle(?:Func)?\(\s*"([^"]+)"/.exec(line);
      if (m) {
        const raw = m[1];
        const sp = raw.indexOf(' ');
        const method = sp > 0 && METHODS.test(raw.slice(0, sp)) ? raw.slice(0, sp) : '';
        routes.push({ file: name, line: i + 1, raw, method, path: sp > 0 ? raw.slice(sp + 1) : raw });
      }
      const svc = /pptsv1connect\.New([A-Za-z]+)Handler/.exec(line);
      if (svc) connect.push({ service: svc[1], file: name, line: i + 1 });
    });
  }
  return { routes, connect };
}

/* ---------- 2. 分组匹配（最长前缀优先） ---------- */
function matchGroup(path, groups) {
  let best = null;
  let bestLen = -1;
  for (const [id, g] of Object.entries(groups)) {
    for (const p of g.prefixes || []) {
      // exact 分组（如 "/" 的 SPA 兜底）只做全等匹配：否则 "/" 的 startsWith 恒真，
      // 会把所有未登记路由悄悄收进去，门禁就变成了永远通过的摆设。
      const hit = g.exact ? path === p : path === p || path.startsWith(p);
      if (hit && p.length > bestLen) {
        best = id;
        bestLen = p.length;
      }
    }
  }
  return best;
}

// route-inventory.json 是人手编辑的，语法错误（尾随逗号最常见）必须给出可定位的提示，
// 而不是抛一段 SyntaxError 堆栈让人去猜。
let inv;
try {
  inv = JSON.parse(readFileSync(INV_PATH, 'utf8'));
} catch (e) {
  console.error(`ERROR route-inventory.json 解析失败：${e.message}`);
  console.error('   本文件手写编辑，最常见是对象最后一项多了尾随逗号。');
  process.exit(1);
}
const { routes, connect } = extractRoutes();
const groups = inv.groups || {};
const errors = [];
const warnings = [];

/* ---------- 3. 逐条归类 ---------- */
const byGroup = {};
const unmatched = [];
for (const r of routes) {
  const gid = matchGroup(r.path, groups);
  if (!gid) {
    unmatched.push(r);
    continue;
  }
  (byGroup[gid] ||= []).push(r);
}

/* ---------- 4. 重复注册（条件分支）识别 ---------- */
const seen = new Map();
for (const r of routes) {
  const k = r.raw;
  seen.set(k, (seen.get(k) || 0) + 1);
}
const conditional = [...seen.entries()].filter(([, n]) => n > 1).map(([k]) => k);

/* ---------- 5. 校验 ---------- */
for (const r of unmatched) {
  errors.push(`未登记分组：${r.raw}（${r.file}:${r.line}）—— 请在 route-inventory.json 登记所属分组、处置与理由`);
}
for (const [id, g] of Object.entries(groups)) {
  if (!g.name) errors.push(`分组 ${id} 缺 name`);
  if (!g.reason) errors.push(`分组 ${id} 缺 reason（必须说明为什么留/为什么迁，否则清单没有价值）`);
  if (!g.disposition) errors.push(`分组 ${id} 缺 disposition`);
  if (!g.prefixes?.length) errors.push(`分组 ${id} 缺 prefixes`);
  if (!byGroup[id]?.length) warnings.push(`分组 ${id} 已无匹配路由（登记腐烂，建议删除或修正 prefixes）`);
}

// 双写分诊：登记的 HTTP 端点必须仍然存在，否则分诊结论已失真。
const routeKeys = new Set(routes.map((r) => r.raw));
for (const d of inv.duplicates || []) {
  if (!d.reason) errors.push(`双写「${d.capability}」缺 reason`);
  for (const h of d.http || []) {
    if (!routeKeys.has(h)) errors.push(`双写「${d.capability}」登记的 HTTP 端点已不存在：${h}（分诊结论失真）`);
  }
}

/* ---------- 6. 输出 ---------- */
if (REPORT) {
  console.log(`原生 HTTP 路由 ${routes.length} 条 / Connect 服务 ${connect.length} 个\n`);
  for (const [id, g] of Object.entries(groups)) {
    const list = byGroup[id] || [];
    console.log(`## ${id} — ${g.name} [${g.disposition}]（${list.length} 条）`);
    console.log(`   ${g.reason}`);
    for (const r of list) console.log(`   ${r.method.padEnd(6)} ${r.path}   ${r.file}:${r.line}`);
    console.log('');
  }
  console.log('Connect 服务：' + connect.map((c) => c.service).join(', '));
  if (conditional.length) {
    console.log(`\n条件互斥注册 ${conditional.length} 条（同路径注册多次 = 配置缺失时挂 503 降级，非双写）：`);
    for (const c of conditional) console.log('   ' + c);
  }
  process.exit(0);
}

console.log(`路由 ${routes.length} 条 / Connect 服务 ${connect.length} 个 / 分组 ${Object.keys(groups).length} 个`);
console.log(`条件互斥注册 ${conditional.length} 条；双写分诊 ${(inv.duplicates || []).length} 组`);
for (const w of warnings) console.log(`WARN  ${w}`);
for (const e of errors) console.log(`ERROR ${e}`);
if (errors.length) {
  console.log(`\n门禁未通过（${errors.length} 项）。新增路由必须登记分组与处置；`);
  console.log('   用 node route-inventory-check.mjs --report 查看当前全量清单。');
  process.exit(1);
}
console.log('route inventory: OK');
