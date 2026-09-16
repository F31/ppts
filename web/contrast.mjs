// V-M2 对比度门禁：零依赖 WCAG 2.1 对比度契约校验。
//
// 范围（与实施计划 §视觉基线专项 V-M2 一致）：校验 V-M1 设计令牌体系中
// 「可读文本令牌」(--text-*) 在【深色】与【浅色】两套值下，相对于所有
// 表面背景令牌（--bg-*）及页面基底背景，正文对比度 ≥ 4.5:1。
//
// 说明：
//  - 仅校验 token 契约（确定性、无级联猜测、无假阳性），不扫描字面量规则；
//    浅色模式下「深色字面量文本未主题化」与「状态色 chip 在浅色微调」属独立
//    遗留，不在本门禁阻断范围（见计划文档 R 注记）。
//  - 背景令牌若为半透明，合成到页面基底背景以得到有效实色。
//
// 用法：node contrast.mjs   （在 web/ 目录下运行，接入 npm run build 前置）
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dir = dirname(fileURLToPath(import.meta.url));
const STYLES = join(__dir, 'src', 'styles.css');
const THEME = join(__dir, 'src', 'theme.css');

const TEXT = ['--text-strong', '--text-body', '--text-muted', '--text-faint', '--text-dim', '--text-faintest'];
const BG = [
  '--bg-shell', '--bg-surface', '--bg-elevated', '--bg-input', '--bg-input-2',
  '--bg-item', '--bg-row', '--bg-sunken', '--bg-surface-2', '--bg-topbar',
  '--bg-user-trigger', '--bg-inset',
];
const THRESHOLD = 4.5;

/* ---------- 颜色解析 ---------- */
function parseColor(v) {
  v = (v || '').trim();
  if (!v || v === 'transparent') return { r: 0, g: 0, b: 0, a: 0 };
  if (v.startsWith('#')) {
    let h = v.slice(1);
    if (h.length === 3) h = h.split('').map((c) => c + c).join('');
    if (h.length !== 6 && h.length !== 8) return null;
    return { r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16), a: h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1 };
  }
  const rgb = v.match(/^rgba?\(([^)]+)\)$/i);
  if (rgb) {
    const p = rgb[1].split(',').map((s) => s.trim());
    return { r: +p[0], g: +p[1], b: +p[2], a: p[3] !== undefined ? parseFloat(p[3]) : 1 };
  }
  return null;
}
function composite(fg, bg) {
  if (fg.a >= 1) return { r: fg.r, g: fg.g, b: fg.b };
  const a = fg.a;
  return { r: Math.round(fg.r * a + bg.r * (1 - a)), g: Math.round(fg.g * a + bg.g * (1 - a)), b: Math.round(fg.b * a + bg.b * (1 - a)) };
}
function relLum({ r, g, b }) {
  const lin = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}
function ratio(c1, c2) {
  const hi = Math.max(relLum(c1), relLum(c2));
  const lo = Math.min(relLum(c1), relLum(c2));
  return (hi + 0.05) / (lo + 0.05);
}

/* ---------- 提取 :root 深色 与 浅色重定义 ---------- */
function stripComments(t) { return t.replace(/\/\*[\s\S]*?\*\//g, ''); }
function topRules(text) {
  const out = [];
  const re = /([^{}]+)\s*\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(text))) {
    const sel = m[1].split().join(' ').trim();
    if (sel.startsWith('@')) continue;
    out.push({ sel, decls: m[2] });
  }
  return out;
}
function declMap(body) {
  const m = {};
  for (const d of body.split(';')) {
    if (!d.includes(':')) continue;
    const i = d.indexOf(':');
    const p = d.slice(0, i).trim();
    const v = d.slice(i + 1).trim();
    if (p && v) m[p] = v;
  }
  return m;
}
function buildVarMap(text, prefix) {
  const map = {};
  for (const r of topRules(text)) {
    if (r.sel !== prefix) continue;
    for (const [k, v] of Object.entries(declMap(r.decls))) if (k.startsWith('--')) map[k] = v;
  }
  return map;
}
function resolveVar(v, darkMap, lightMap) {
  const m = v.match(/^var\((--[a-z0-9-]+)(?:\s*,\s*([^)]+))?\)$/i);
  if (!m) return { dark: v, light: v };
  const tok = m[1];
  const fb = m[2];
  const dv = darkMap[tok];
  const lv = lightMap[tok];
  return { dark: dv !== undefined ? dv : fb || v, light: lv !== undefined ? lv : dv !== undefined ? dv : fb || v };
}

const styles = stripComments(readFileSync(STYLES, 'utf8'));
const theme = stripComments(readFileSync(THEME, 'utf8'));
const darkMap = buildVarMap(styles, ':root');
const lightMap = buildVarMap(theme, 'html[data-theme="light"]');
const PAGE = { dark: { r: 16, g: 19, b: 26 }, light: { r: 238, g: 241, b: 246 } };

const fails = [];
for (const t of TEXT) {
  const dv = darkMap[t];
  const lv = lightMap[t];
  if (!dv || !lv) { console.log(`WARN 令牌缺失: ${t}`); continue; }
  const fgDark = parseColor(dv);
  const fgLight = parseColor(lv);
  if (!fgDark || !fgLight) continue;
  const bgs = [{ name: 'page-base', dark: PAGE.dark, light: PAGE.light }, ...BG.map((b) => {
    const rb = resolveVar(darkMap[b] || '', darkMap, lightMap);
    const lb = resolveVar(lightMap[b] || darkMap[b] || '', darkMap, lightMap);
    return { name: b, dark: parseColor(rb.dark) || PAGE.dark, light: parseColor(lb.light) || PAGE.light };
  })];
  for (const bg of bgs) {
    const realDark = bg.dark.a < 1 ? composite(bg.dark, PAGE.dark) : bg.dark;
    const realLight = bg.light.a < 1 ? composite(bg.light, PAGE.light) : bg.light;
    const rd = ratio(fgDark, realDark);
    const rl = ratio(fgLight, realLight);
    const min = Math.min(rd, rl);
    if (min < THRESHOLD) {
      fails.push({ token: t, bg: bg.name, rd: rd.toFixed(2), rl: rl.toFixed(2), min: min.toFixed(2) });
    }
  }
}

console.log('=== 对比度门禁（WCAG 2.1，正文阈值 4.5:1，仅校验 --text-* 令牌契约）===');
if (fails.length === 0) {
  console.log('OK：全部文本令牌在两套主题、所有表面背景上对比度 ≥ 4.5:1。');
} else {
  for (const f of fails) {
    console.log(`FAIL ${f.token}  on ${f.bg}  dark=${f.rd}:1  light=${f.rl}:1  (min ${f.min})`);
  }
}
console.log(`\n正文失败项：${fails.length}`);
process.exit(fails.length > 0 ? 1 : 0);
