// V-M3 i18n 门禁：零引用词条（孤儿）+ 中英键集合对等 契约校验。
//
// 为什么需要（`tsc -b` 结构上管不到）：
//   i18n key 是 JSON 里的普通字符串，而 t() 的签名是 t(key: string)
//   （src/i18n/index.tsx:27），两边的对应关系完全**不在类型系统里** ——
//   一条死文案对编译器既不是错误也不是警告。于是「改名 key / 删掉面板 /
//   先写文案」这三种单向操作会不断沉淀零引用词条：2026-09-22 一次分诊
//   清出 99 条（删 82 / 留 17，判据见 docs/项目遗留与历史决定.md §5）。
//
// 口径（确定性优先，倾向宽松）：
//   - t() 只有一条路径 `messages?.[key] ?? key`，故「key 是否被引用」可静态判定，
//     **唯一例外是运行时拼接** —— 模板字符串 `${` 左侧的静态段、紧邻 `+` 的字面量。
//     这类走「动态前缀」放行，并在报告里显式列出，不静默。
//   - **不剥离注释**：注释里写到的 key 也算「被引用」。这是宽松方向 —— 宁可漏报
//     （死文案多留一轮），不可误报（阻塞构建会逼人绕过门禁）。
//   - 门禁**不判断**「该删还是该留」：只报「新增孤儿」。存量保留项登记在
//     i18n-orphans.baseline.json 里；分诊（死文案 → 删 / 未接线能力 → 留）
//     必须人工完成，机器判不了「后端有没有对应端点」。
//
// 用法：
//   node i18n-check.mjs                     校验（退出码非 0 = 有新增孤儿或中英不对等）
//   node i18n-check.mjs --update-baseline   分诊完成后登记当前孤儿与动态前缀
//   （package.json 里为 npm run i18n:check；当前**未**挂进 build，沿用「先观察一轮」）
import { readFileSync, readdirSync, statSync, writeFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, relative } from 'node:path';

const __dir = dirname(fileURLToPath(import.meta.url));
const SRC = join(__dir, 'src');
const ZH_PATH = join(SRC, 'i18n', 'zh.json');
const EN_PATH = join(SRC, 'i18n', 'en.json');
const BASELINE_PATH = join(__dir, 'i18n-orphans.baseline.json');
const UPDATE = process.argv.includes('--update-baseline');

// 低于此长度的拼接段不足以识别命名空间，忽略（宽松方向：不轻易放行整族）。
const MIN_PREFIX = 6;

/* ---------- 源码遍历 ---------- */
function collectSources(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) out.push(...collectSources(path));
    else if (/\.(ts|tsx)$/.test(path)) out.push(path);
  }
  return out.sort();
}

/* ---------- 引用点收集 ---------- */
const exact = new Set(); // 完整字面量（单/双引号串 + 无 ${} 的模板串）
const callKeys = new Set(); // t('...') 的实参（用于「引用了不存在的 key」观察项）
const dynamicPrefixes = new Map(); // 自动检测到的运行时拼接前缀 → 首个出现文件
const STRING_RE = /"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/g;
const TEMPLATE_RE = /`(?:[^`\\]|\\.)*`/g;
const PLUS_RE = /(['"])([^'"\n]*)\1\s*\+|\+\s*(['"])([^'"\n]*)\3/g;
const CALL_RE = /\bt\(\s*(['"])([^'"\n]+)\1/g;

// 命名空间前缀形态：以字母开头、含点、只用 [A-Za-z0-9_.]。
// 含点这一条排掉 `'health-tag ' + x` 这类非 key 空间的拼接噪声。
function isNsPrefix(s) {
  return s.length >= MIN_PREFIX && s.includes('.') && /^[A-Za-z][A-Za-z0-9_.]*$/.test(s);
}

for (const file of collectSources(SRC)) {
  const src = readFileSync(file, 'utf8');
  const rel = relative(__dir, file);

  for (const m of src.matchAll(STRING_RE)) exact.add(m[0].slice(1, -1));

  for (const m of src.matchAll(TEMPLATE_RE)) {
    const raw = m[0].slice(1, -1);
    const cut = raw.indexOf('${');
    if (cut < 0) {
      exact.add(raw);
      continue;
    }
    const head = raw.slice(0, cut);
    if (isNsPrefix(head) && !dynamicPrefixes.has(head)) dynamicPrefixes.set(head, rel);
  }

  for (const m of src.matchAll(PLUS_RE)) {
    const lit = m[2] !== undefined ? m[2] : m[4];
    if (lit && isNsPrefix(lit) && !dynamicPrefixes.has(lit)) dynamicPrefixes.set(lit, rel);
  }

  for (const m of src.matchAll(CALL_RE)) callKeys.add(m[2]);
}

/* ---------- 读 i18n 与基线 ---------- */
const zh = JSON.parse(readFileSync(ZH_PATH, 'utf8'));
const en = JSON.parse(readFileSync(EN_PATH, 'utf8'));
const baseline = existsSync(BASELINE_PATH)
  ? JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
  : { dynamicPrefixes: {}, orphans: {} };

// 人工声明的动向前缀（基线里）与自动检测到的取并集 —— 自动检测兜常见情形，
// 人工兜「拼接写法复杂到静态看不出来」的极端情形。
const declaredPrefixes = new Set([
  ...Object.keys(baseline.dynamicPrefixes || {}),
  ...dynamicPrefixes.keys()
]);
const isDynamic = (k) => {
  for (const p of declaredPrefixes) if (k.startsWith(p)) return true;
  return false;
};

/* ---------- 判定 ---------- */
const orphanAll = Object.keys(zh).filter((k) => !exact.has(k) && !isDynamic(k));
const allowed = new Set(Object.keys(baseline.orphans || {}));
const newOrphans = orphanAll.filter((k) => !allowed.has(k));
const staleBaseline = [...allowed].filter((k) => !orphanAll.includes(k));
const droppedKeys = staleBaseline.filter((k) => !(k in zh)); // 已从 json 删掉
const wiredKeys = staleBaseline.filter((k) => k in zh); // 已接线/被引用

const zhKeys = Object.keys(zh);
const enKeys = Object.keys(en);
const onlyZh = zhKeys.filter((k) => !(k in en));
const onlyEn = enKeys.filter((k) => !(k in zh));

// 观察项：源码里 t('x.y') 但 json 无此 key → 界面会原样显示 key。
// 本轮只报不阻塞（待观察一轮确认无误报，再考虑升为 FAIL）。
const missingKeys = [...callKeys].filter((k) => !(k in zh)).sort();

/* ---------- --update-baseline ---------- */
if (UPDATE) {
  const sortedOrphans = {};
  for (const k of [...orphanAll].sort()) sortedOrphans[k] = (baseline.orphans || {})[k] || '';
  const sortedPrefixes = {};
  for (const p of [...declaredPrefixes].sort()) {
    sortedPrefixes[p] =
      (baseline.dynamicPrefixes || {})[p] ||
      `运行时拼接（自动检测于 ${dynamicPrefixes.get(p) || '代码中'}）`;
  }
  writeFileSync(
    BASELINE_PATH,
    JSON.stringify({ dynamicPrefixes: sortedPrefixes, orphans: sortedOrphans }, null, 2) + '\n',
    'utf8'
  );
  console.log(`已写入基线：${relative(__dir, BASELINE_PATH)}`);
  console.log(`  动态前缀 ${Object.keys(sortedPrefixes).length} 条，存量孤儿 ${Object.keys(sortedOrphans).length} 条`);
  if (droppedKeys.length) console.log(`  清理已删除条目 ${droppedKeys.length} 条：${droppedKeys.join('、')}`);
  if (wiredKeys.length) console.log(`  清理已接线条目 ${wiredKeys.length} 条：${wiredKeys.join('、')}`);
  console.log('  请为新增条目补 reason 后提交（reason 说明「为什么有意保留」）。');
  process.exit(0);
}

/* ---------- 报告 ---------- */
console.log('=== i18n 门禁（零引用词条 + 中英键集合对等）===');

if (dynamicPrefixes.size) {
  console.log('动态前缀放行（运行时拼接，静态不可见）：');
  for (const [p, file] of [...dynamicPrefixes].sort()) {
    const n = zhKeys.filter((k) => k.startsWith(p)).length;
    console.log(`  ${p}  ← ${file}（覆盖 ${n} 条）`);
  }
}

let fails = 0;

if (newOrphans.length) {
  fails += newOrphans.length;
  console.log(`\nFAIL 新增零引用词条 ${newOrphans.length} 条（未登记在基线）：`);
  for (const k of newOrphans) console.log(`  ${k}  ${JSON.stringify(zh[k]).slice(0, 48)}`);
  console.log('  处理：死文案 → 直接删；有意保留（如后端已有端点的未接线能力）→ 用 --update-baseline 登记并补 reason。');
}

if (onlyZh.length || onlyEn.length) {
  fails += 1;
  console.log(`\nFAIL 中英键集合不对等：only-zh ${onlyZh.length} / only-en ${onlyEn.length}`);
  for (const k of onlyZh.slice(0, 20)) console.log(`  only-zh  ${k}`);
  for (const k of onlyEn.slice(0, 20)) console.log(`  only-en  ${k}`);
}

if (staleBaseline.length) {
  console.log(`\nWARN 基线过期 ${staleBaseline.length} 条（建议用 --update-baseline 清理）：`);
  for (const k of droppedKeys) console.log(`  已从 json 删除  ${k}`);
  for (const k of wiredKeys) console.log(`  已接线/被引用  ${k}`);
}

if (missingKeys.length) {
  console.log(`\nINFO 源码引用了 json 里不存在的 key ${missingKeys.length} 条（界面会原样显示 key）：`);
  for (const k of missingKeys.slice(0, 20)) console.log(`  ${k}`);
  console.log('  （本轮不阻塞，待观察一轮确认无误报）');
}

console.log('');
if (fails === 0) {
  console.log(
    `OK：无新增零引用词条（存量基线 ${allowed.size} 条、当前孤儿 ${orphanAll.length} 条），` +
      `中英键集合对等（${zhKeys.length} / ${enKeys.length}）。`
  );
} else {
  console.log(`失败项：${newOrphans.length} 条新增孤儿 + ${onlyZh.length || onlyEn.length ? 1 : 0} 项中英不对等。`);
}
process.exit(fails > 0 ? 1 : 0);
