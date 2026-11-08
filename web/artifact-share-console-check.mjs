#!/usr/bin/env node
/**
 * artifact-share-console-check —— 作者侧成品分享控制台的静态判据。
 *
 * 守的不是「编译过不过」，而是四件编译看不见的事：
 *   1. 请求只在 api/*.ts（组件里出现 fetch 就是又开了一条自制错误链）；
 *   2. 前后端路径同源（api-contract-check 会查，但这里把成品分享这一组单独钉住）；
 *   3. 四条诚实口径不能被重构掉：在线范围 / 留存窗口 / 完播率分母 / 采集关闭说明；
 *   4. 采集关闭或请求失败时**不得渲染数字区**——把 0 显示成「0 次观看」是谎报。
 *
 * 门禁没有空过路径：checks 为 0 时 exit 2（与 V18/V19/V20 同一纪律）。
 * `--self-test` 用字符串变异证明每条关键判据真的会红，不是恒真的摆设。
 */
import { readFileSync } from 'node:fs';

const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');

function sources() {
  return {
    panel: read('./src/components/ArtifactSharePanel.tsx'),
    library: read('./src/pages/Library.tsx'),
    api: read('./src/api/artifactShare.ts'),
    go: read('../internal/api/artifact_share.go') + read('../internal/api/artifact_share_stats.go'),
    zh: JSON.parse(read('./src/i18n/zh.json')),
    en: JSON.parse(read('./src/i18n/en.json'))
  };
}

// 归一规则与 api-contract-check.mjs 一致：模板变量与 Go 的 {param} 都折成 *。
function normalise(path) {
  return path
    .replace(/(^|\/)\$\{[^}]*\}/g, (_, pre) => `${pre}*`)
    .replace(/\$\{[^}]*\}/g, '')
    .replace(/\{[^}]+\}/g, '*')
    .split('?')[0]
    .replace(/\/$/, '');
}

function goRoutes(go) {
  const out = new Set();
  for (const m of go.matchAll(/mux\.Handle\(\s*"(?:GET|POST|PUT|PATCH|DELETE) ([^"]+)"/g)) {
    out.add(normalise(m[1]));
  }
  return out;
}

const NEW_KEYS = [
  'library.share', 'library.shareHint',
  'artifactShare.eyebrow', 'artifactShare.title', 'artifactShare.scopeNote',
  'artifactShare.retentionNote', 'artifactShare.denominatorNote', 'artifactShare.telemetryOff',
  'artifactShare.noLinks', 'artifactShare.create', 'artifactShare.createFailed',
  'artifactShare.revoke', 'artifactShare.revokeFailed', 'artifactShare.copy',
  'artifactShare.copied', 'artifactShare.copyFailed', 'artifactShare.revoked',
  'artifactShare.expiryNever', 'artifactShare.expiryAt', 'artifactShare.passwordSet',
  'artifactShare.passwordOptional', 'artifactShare.showStats', 'artifactShare.hideStats',
  'artifactShare.statsFailed', 'artifactShare.loadFailed', 'artifactShare.views',
  'artifactShare.sessions', 'artifactShare.completed', 'artifactShare.completionRate',
  'artifactShare.bucket', 'artifactShare.noEvents', 'artifactShare.days',
  'artifactShare.days7', 'artifactShare.days30', 'artifactShare.days90'
];

function buildChecks(s) {
  const checks = [];
  const check = (name, fn) => checks.push({ name, fn });
  const assert = (cond, msg) => { if (!cond) throw new Error(msg); };

  check('组件与页面零 fetch：请求只走 api/*.ts', () => {
    assert(!/\bfetch\s*\(/.test(s.panel), 'ArtifactSharePanel 出现了 fetch');
    assert(!/\bfetch\s*\(/.test(s.library), 'Library 出现了 fetch');
    assert(/getJSON|postJSON/.test(s.api), 'api/artifactShare.ts 必须走统一 HTTP helper');
  });

  check('成品分享四条路径都能在 Go 路由表里找到', () => {
    const routes = goRoutes(s.go);
    for (const p of [
      '/projects/{pid}/artifacts/{aid}/shares',
      '/projects/{pid}/artifacts/{aid}/shares/{linkId}/revoke',
      '/projects/{pid}/artifacts/{aid}/shares/{linkId}/stats'
    ]) {
      assert(routes.has(normalise(p)), `后端缺少路由 ${p}`);
    }
    for (const p of [
      '/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares',
      '/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares/${encodeURIComponent(linkId)}/revoke',
      '/projects/${encodeURIComponent(projectId)}/artifacts/${encodeURIComponent(artifactId)}/shares/${encodeURIComponent(linkId)}/stats?days=${encodeURIComponent(String(days))}'
    ]) {
      assert(s.api.includes(p), `前端少了路径字面量 ${p}`);
      assert(routes.has(normalise(p)), `前端路径归一后后端不存在：${p}`);
    }
  });

  check('四条诚实口径键齐全且中英对等', () => {
    for (const key of NEW_KEYS) {
      assert(typeof s.zh[key] === 'string', `zh.json 缺 ${key}`);
      assert(typeof s.en[key] === 'string', `en.json 缺 ${key}`);
    }
    for (const key of ['artifactShare.scopeNote', 'artifactShare.retentionNote',
      'artifactShare.denominatorNote', 'artifactShare.telemetryOff']) {
      assert(s.panel.includes(`t('${key}'`), `面板没有渲染 ${key}`);
    }
    assert(/离线|offline/i.test(s.zh['artifactShare.scopeNote'] + s.en['artifactShare.scopeNote']), '范围说明必须点名离线不上报');
    assert(/不是累计全量|not an all-time total/.test(s.zh['artifactShare.retentionNote'] + s.en['artifactShare.retentionNote']), '必须明说不是全量');
    assert(/分母|denominator|独立会话|unique sessions/.test(s.zh['artifactShare.denominatorNote'] + s.en['artifactShare.denominatorNote']), '必须写明完播率分母');
  });

  check('采集关闭 / 请求失败都必须早退，且早退分支不渲染数字', () => {
    const metrics = s.panel.indexOf('artifact-share-metrics');
    const off = s.panel.indexOf('if (!data.telemetryEnabled)');
    const err = s.panel.indexOf('if (state.error)');
    const loading = s.panel.indexOf('if (!state || state.loading)');
    assert(metrics > 0 && off > 0 && err > 0 && loading > 0, '四个分支有一个消失');
    assert(off < metrics, '采集关闭判据必须在数字区之前');
    assert(err < metrics, '失败判据必须在数字区之前');
    assert(loading < metrics, '加载中判据必须在数字区之前');
    // 早退块里不能出现指标取值：把 0 显示成「0 次观看」就是这条要挡的谎报。
    const offBlock = s.panel.slice(off, s.panel.indexOf('const { stats }', off));
    assert(!/stats\./.test(offBlock), '采集关闭分支渲染了指标');
    assert(/telemetryOff/.test(offBlock), '采集关闭分支没有说明文案');
    const errBlock = s.panel.slice(err, s.panel.indexOf('const data', err));
    assert(!/stats\./.test(errBlock), '失败分支渲染了指标');
  });

  check('切换统计窗口必须丢掉旧缓存', () => {
    assert(/setStatsByLink\(\{\}\)/.test(s.panel), '窗口变化仍复用旧数据，标签与数字会不一致');
    assert(/\[expanded, days, loadStats\]/.test(s.panel), '窗口变化没有触发重新加载');
  });

  check('Library 只对 html_bundle 且有权者显示分享入口', () => {
    const marker = s.library.indexOf('className="icon-btn artifact-share"');
    assert(marker > 0, '卡片缺少分享按钮');
    const guard = s.library.slice(Math.max(0, marker - 600), marker);
    assert(/canShare && a\.format === 'html_bundle'/.test(guard), '按钮守卫不是「有权限且仅 html_bundle」');
    assert(!/a\.format === 'html_single'/.test(guard), 'html_single 不能建 /s/ 链接，不该出现在守卫里');
    assert(/<ArtifactSharePanel/.test(s.library), '面板没有挂进 Library');
  });

  check('统计窗口上限与后端常量一致', () => {
    const go = read('../internal/api/artifact_share_stats.go');
    const max = Number(/shareStatsMaxDays\s*=\s*(\d+)/.exec(go)?.[1] ?? 0);
    const defaults = Number(/shareStatsDefaultDays\s*=\s*(\d+)/.exec(go)?.[1] ?? 0);
    assert(max > 0 && defaults > 0, '后端常量解析失败');
    assert(s.panel.includes(`useState<number>(${defaults})`), '前端默认窗口与后端不一致');
    for (const d of [7, 30, 90]) {
      assert(s.panel.includes(`value={${d}}`), `前端缺窗口 ${d}`);
      assert(d <= max, `前端窗口 ${d} 超过后端上限 ${max}`);
    }
  });

  return checks;
}

function runChecks(checks) {
  let failed = 0;
  for (const c of checks) {
    try {
      c.fn();
      console.log(`ok - ${c.name}`);
    } catch (e) {
      failed += 1;
      console.error(`not ok - ${c.name}\n    ${e.message}`);
    }
  }
  return failed;
}

const base = sources();
const selfTest = process.argv.includes('--self-test');
if (selfTest) {
  // 三组变异各要打红对应判据；如果哪组还是全绿，说明那条判据是恒真的摆设。
  const mutations = [
    ['删掉采集关闭早退', (s) => ({ ...s, panel: s.panel.replace(/if \(!data\.telemetryEnabled\) \{[\s\S]*?\n  \}/, '') }), '采集关闭 / 请求失败'],
    ['组件里私加 fetch', (s) => ({ ...s, panel: s.panel + '\nvoid fetch("/api/x");\n' }), '组件与页面零 fetch'],
    ['放开 html_single 守卫', (s) => ({ ...s, library: s.library.replace("canShare && a.format === 'html_bundle'", 'canShare') }), 'Library 只对 html_bundle'],
    ['删掉留存窗口说明', (s) => ({ ...s, zh: { ...s.zh, 'artifactShare.retentionNote': '' }, en: { ...s.en, 'artifactShare.retentionNote': '' } }), '四条诚实口径']
  ];
  let bad = 0;
  for (const [name, mutate, expectRed] of mutations) {
    const checks = buildChecks(mutate(base));
    const failed = runChecksSilent(checks);
    const red = checks.filter((c, i) => failed[i]);
    const hit = red.some((c) => c.name.includes(expectRed));
    console.log(`${hit ? 'ok' : 'not ok'} - 变异「${name}」应打红「${expectRed}」`);
    if (!hit) bad += 1;
  }
  process.exit(bad ? 1 : 0);
}

function runChecksSilent(checks) {
  return checks.map((c) => {
    try { c.fn(); return false; } catch { return true; }
  });
}

const checks = buildChecks(base);
if (checks.length === 0) {
  console.error('FAIL：判据列表为空，门禁不能空过');
  process.exit(2);
}
const failed = runChecks(checks);
console.log(`\n${checks.length - failed}/${checks.length} 通过`);
process.exit(failed ? 1 : 0);
