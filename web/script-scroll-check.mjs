// 讲稿栏「只有一层滚动」门禁。
//
// 背景：讲稿面板曾经有三层各自 overflow:auto 的容器（.script-column-body /
// .segment-list / .script-rich-editor）。内容一长，右侧就并排出现两条竖滚动条：
// 一条属于段落列表、一条属于文本框，指针落在哪层决定滚轮滚哪层，看起来像"滚不动"。
// 这类缺陷没有报错、没有接口调用，纯样式回归，靠人眼在特定窗口高度下才看得见。
//
// 契约（与 styles.css 的注释一致）：
//   1. 唯一滚动层是 .script-column-body（overflow-y:auto）。
//   2. .segment-list 与 .script-rich-editor 都不得自成滚动容器
//      （overflow / overflow-y 不能是 auto|scroll）。
//   3. 讲稿卡片与文本框都不允许写死 height:100% —— 固定高度会把内容切成内层滚动。
//
// 例外（不归本门禁管）：<1200px 宽或 <760px 高时，整页退回文档滚动，
// .script-column-body 会被媒体查询改成 overflow: visible —— 那时全页只有文档一条
// 滚动条，同样是"只有一层"。本门禁校验的是基础规则，别把媒体查询里的 override
// 当成违规。
//
// 用法：node script-scroll-check.mjs（在 web/ 目录下运行，接入 npm run build 前置）
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dir = dirname(fileURLToPath(import.meta.url));
const STYLES = join(__dir, 'src', 'styles.css');
const css = readFileSync(STYLES, 'utf8');

// rulesOf：取出选择器**完全等于** selector 的所有规则体（不做级联推断 ——
// 这里要守的是"本文件里没人给这些类写滚动"，而不是最终计算值，零假阳性优先）。
// 先剥注释：注释不带分号，但会被当进选择器文本，导致匹配不上。
const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '');
function rulesOf(selector) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(stripped)) !== null) {
    const selectors = m[1].split(',').map((s) => s.trim());
    if (selectors.includes(selector)) out.push(m[2]);
  }
  return out;
}

// declOf：读某条规则里最后一个同名声明（后写覆盖先写，与级联同向）。
function declOf(body, prop) {
  const re = new RegExp(`(?:^|;)\\s*${prop}\\s*:\\s*([^;]+)`, 'gi');
  let value = null;
  let m;
  while ((m = re.exec(body)) !== null) value = m[1].trim();
  return value;
}

const failures = [];

function expectScroller(selector) {
  const bodies = rulesOf(selector);
  if (bodies.length === 0) {
    failures.push(`${selector}：样式表里找不到这条规则（选择器被改名或删掉了？）`);
    return;
  }
  const ok = bodies.some((body) => /^(auto|scroll)$/i.test(declOf(body, 'overflow-y') || ''));
  if (!ok) failures.push(`${selector}：应保留 overflow-y: auto，它是讲稿栏唯一的滚动层`);
}

function expectNotScroller(selector) {
  for (const body of rulesOf(selector)) {
    for (const prop of ['overflow', 'overflow-y']) {
      const value = declOf(body, prop);
      if (value && /^(auto|scroll)$/i.test(value)) {
        failures.push(
          `${selector}：${prop}: ${value} 会在这里再长出一条滚动条；滚动交给 .script-column-body`
        );
      }
    }
  }
}

function expectNoFixedHeight(selector) {
  for (const body of rulesOf(selector)) {
    const value = declOf(body, 'height');
    if (value && /^100%$/i.test(value)) {
      failures.push(`${selector}：height: 100% 会把内容切成内层滚动，改用 auto 让卡片随内容生长`);
    }
  }
}

expectScroller('.script-column-body');
expectNotScroller('.segment-list');
expectNotScroller('.script-rich-editor');
expectNoFixedHeight('.script-rich-editor');
expectNoFixedHeight('.script-editor');

if (failures.length > 0) {
  console.error('讲稿栏滚动门禁失败：');
  for (const line of failures) console.error(`  ✗ ${line}`);
  process.exit(1);
}

console.log('讲稿栏滚动门禁通过：唯一滚动层 .script-column-body；段落列表与文本框均不独立滚动。');
