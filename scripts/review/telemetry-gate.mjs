#!/usr/bin/env node
/**
 * V20 埋点完整性门禁（设计 §20.8）。
 *
 * 输入是**成品包目录**（share-baseline 造出来的那种），不是源码目录：判据要验的是
 * 「客户拿到的那个包」里有没有埋点、CSP 配得对不对。行为层判据（该不该发、发哪几类）
 * 在 web/share-telemetry-check.mjs 与 internal/api/artifact_share_events_test.go，
 * 这里守的是「构建产物与源码没漂移」+「在线 CSP 交集能放行同源上报」。
 *
 * 失败等级：告警。但**空过必须失败**——cases 为 0 时 exit 2，与 V18/V19 同一纪律。
 *
 * 编号边界：V20 只在 M6-4 分享包门禁体系（设计 §20.8 的 V13–V20）内唯一。
 * 保真链 scripts/fidelity/verify.py 另有一组 V20（矩形焦点遮罩，判据码也是 V20.x），
 * 两者不共用命名空间；读报告时须带体系名，别把「V20.1 埋点符号」和「V20.1 白纱强度」
 * 当成同一条判据。
 */
import { readFileSync, existsSync } from 'node:fs';
import { resolve, join } from 'node:path';

function arg(name) {
  const i = process.argv.indexOf(name);
  return i >= 0 ? process.argv[i + 1] : null;
}
const pkg = resolve(arg('--pkg') || '.');
if (!existsSync(join(pkg, 'app.js')) || !existsSync(join(pkg, 'index.html'))) {
  console.error(`V20: 不是成品包目录（缺 app.js / index.html）：${pkg}`);
  process.exit(2);
}
const app = readFileSync(join(pkg, 'app.js'), 'utf8');
const html = readFileSync(join(pkg, 'index.html'), 'utf8');

const checks = [];
const add = (code, ok, msg) => checks.push({ code, ok: !!ok, msg });

// 引号风格会被 esbuild 换成反引号，所以事件名用宽松匹配；其余符号是标识符，稳定。
// 用 fromCharCode 拼反引号，避免模板字符串里再嵌一个反引号导致语法错误。
const QUOTE_CLASS = '[\'"' + String.fromCharCode(96) + '"]';
const anyQuote = (s) => new RegExp(QUOTE_CLASS + s + QUOTE_CLASS);

// V20.1 会话与批量上报真的进了包（不是只在源码里）。
add('V20.1', /sessionId/.test(app) && /events:/.test(app) && /keepalive/.test(app),
  '包内 app.js 含匿名会话、事件批次与 keepalive 发送');

// V20.2 事件集合封闭：view + 25/50/75/100。
add('V20.2',
  anyQuote('view').test(app) && anyQuote('progress').test(app) && /\[25,50,75,100\]/.test(app),
  '首帧 view 与四个进度桶齐全');

// V20.3 只有在线 /s/<32hex>/ 形态启用，离线 file:// 一律不启用。
add('V20.3',
  /\^\\\/s\\\/\[0-9a-f\]\{32\}\\\/\$/.test(app) &&
  anyQuote('http:').test(app) && anyQuote('https:').test(app),
  '在线路径与协议判据存在（file:// 不在白名单）');

// V20.4 隐私开关：DNT / GPC / ?privacy=1。
add('V20.4',
  /doNotTrack/.test(app) && /globalPrivacyControl/.test(app) && anyQuote('privacy').test(app),
  'DNT / GPC / privacy=1 三个关闭信号齐全');

// V20.5 四个接入点都在（首帧、逐帧、拖动跳过、播完补齐）。
add('V20.5',
  /reportView/.test(app) && /tick:/.test(app) && /seek:/.test(app) && /finish:/.test(app),
  '首帧曝光 / 播放推进 / 拖动跳过 / 播完补齐均接线');

// V20.6 在线 CSP 交集：meta 必须显式给 connect-src 'self'，否则响应头那条会被 meta 抵消。
const csp = (html.match(/content="([^"]*default-src[^"]*)"/) || [])[1] || '';
add('V20.6', /connect-src 'self'/.test(csp), `meta CSP 放行同源上报（实际：${csp || '未找到 CSP'}）`);

// V20.7 零外链不被埋点破坏：CSP 里没有远程来源，包内也没有外链资源引用。
const hasRemoteRef = /(?:src|href)\s*=\s*["'](?:https?:|\/\/)/i.test(html);
add('V20.7', !hasRemoteRef && !/connect-src[^;]*\*/.test(csp) && !/connect-src[^;]*https?:/.test(csp),
  'V14 零外链保持：无远程资源引用、connect-src 未放宽');

// V20.8 副作用只有一处，防止「离线也发一个请求」这类漏改。
const fetchCount = (app.match(/\bfetch\(/g) || []).length;
add('V20.8', fetchCount === 1, `包内只有一处上报 fetch（实际 ${fetchCount} 处）`);

if (checks.length === 0) {
  console.error('V20: 0 条判据，拒绝空过');
  process.exit(2);
}
for (const c of checks) {
  console.log(`${c.ok ? 'PASS' : 'FAIL'} ${c.code}  ${c.msg}`);
}
const failed = checks.filter((c) => !c.ok);
console.log(`\nV20 埋点完整性：${checks.length - failed.length}/${checks.length} 通过`);
process.exit(failed.length > 0 ? 1 : 0);
