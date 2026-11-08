#!/usr/bin/env node
/**
 * share-telemetry-check —— V20「埋点完整性」的前端判据。
 *
 * 守的不是「有没有发请求」，而是「该发的时候发齐、不该发的时候一个都不发」：
 *   - 在线 /s/<token>/ 才启用；file://、门禁静态托管、DNT/GPC/privacy=1 全关；
 *   - 事件集合封闭：view + 25/50/75/100；
 *   - 拖动跳过的桶不许补报（埋点说实际发生的事，不是把集合凑齐）；
 *   - 源码级接线断言：函数写了但没接进播放器、或 CSP 少了 connect-src，
 *     等于没做——Go/TS 编译都不会替你发现这两类漏接。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';

const t = await import('./src/sharePlayer/telemetry.ts');

const baseEnv = (over = {}) => ({
  protocol: 'https:',
  pathname: '/s/0123456789abcdef0123456789abcdef/',
  search: '',
  host: 'ppts.example.com',
  referrer: '',
  doNotTrack: null,
  globalPrivacyControl: false,
  ...over
});

const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');

test('仅在线分享形态启用：离线/隐私一律零请求', () => {
  assert.equal(t.shouldReportTelemetry(baseEnv()), true);
  assert.equal(t.shouldReportTelemetry(baseEnv({ protocol: 'http:' })), true);
  // 这四条是「自包含成品包断网可播」的硬边界。
  assert.equal(t.shouldReportTelemetry(baseEnv({ protocol: 'file:' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ protocol: 'blob:' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ pathname: '/tmp/pkg/index.html' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ pathname: '/s/not-a-token/' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ pathname: '/s/0123456789abcdef0123456789abcdef' })), false);
  // 隐私信号：DNT / GPC / 显式 privacy=1
  assert.equal(t.shouldReportTelemetry(baseEnv({ doNotTrack: '1' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ globalPrivacyControl: true })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ search: '?privacy=1' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ search: '?privacy=true&x=1' })), false);
  assert.equal(t.shouldReportTelemetry(baseEnv({ doNotTrack: '0', search: '?privacy=0' })), true);
});

test('端点是同源相对路径，不引入任何主机或协议', () => {
  assert.equal(
    t.telemetryEndpoint('/s/0123456789abcdef0123456789abcdef/'),
    '/s/0123456789abcdef0123456789abcdef/events'
  );
  assert.equal(t.telemetryEndpoint('/tmp/pkg/index.html'), null);
  const endpoint = t.telemetryEndpoint('/s/0123456789abcdef0123456789abcdef/');
  assert.doesNotMatch(endpoint, /^https?:|^\/\//);
});

test('分享来源只取显式参数或 referrer 主机，不泄漏完整 URL', () => {
  assert.equal(t.shareSource(baseEnv({ search: '?utm_source=wechat' })), 'wechat');
  assert.equal(t.shareSource(baseEnv({ search: '?from=moments' })), 'moments');
  assert.equal(t.shareSource(baseEnv()), 'direct');
  assert.equal(t.shareSource(baseEnv({ referrer: 'https://ppts.example.com/library' })), 'internal');
  assert.equal(t.shareSource(baseEnv({ referrer: 'https://t.cn/abc?token=SECRET' })), 't.cn');
  assert.equal(t.shareSource(baseEnv({ search: '?utm_source=https://evil/x?a=1' })), '');
  assert.equal(t.shareSource(baseEnv({ search: '?utm_source=' + 'x'.repeat(200) })), 'x'.repeat(64));
});

test('进度桶按真实推进跨越，同一桶只报一次', () => {
  const seen = new Set();
  assert.deepEqual(t.crossedBuckets(0, seen), []);
  assert.deepEqual(t.crossedBuckets(0.249, seen), []);
  assert.deepEqual(t.crossedBuckets(0.25, seen), [25]);
  seen.add(25);
  assert.deepEqual(t.crossedBuckets(0.5, seen), [50]);
  seen.add(50);
  // 一次跳到 80%（时钟跳变）会同时跨过 75；由 seek 标记兜住，这里只验纯函数语义。
  assert.deepEqual(t.crossedBuckets(0.8, seen), [75]);
  assert.deepEqual(t.crossedBuckets(NaN, seen), []);
  assert.deepEqual(t.crossedBuckets(-1, seen), []);
});

test('拖动跳过的桶不再补报：埋点不伪造观看', () => {
  const seen = new Set();
  t.markSkippedBuckets(0.8, seen);
  assert.deepEqual(t.crossedBuckets(0.8, seen), []);
  assert.deepEqual(t.crossedBuckets(1, seen), [100]);
  // 从 0 开始正常播放则四类齐全。
  const fresh = new Set();
  const all = [];
  for (const ratio of [0.25, 0.5, 0.75, 1]) {
    for (const b of t.crossedBuckets(ratio, fresh)) {
      fresh.add(b);
      all.push(b);
    }
  }
  assert.deepEqual(all, [25, 50, 75, 100]);
});

test('事件名集合封闭且与后端白名单一致', () => {
  assert.deepEqual([...t.TELEMETRY_EVENT_NAMES], [
    'view', 'progress_25', 'progress_50', 'progress_75', 'progress_100'
  ]);
  const go = read('../internal/api/artifact_share_events.go');
  assert.match(go, /shareProgressBuckets\s*=\s*\[\]int\{25, 50, 75, 100\}/);
  assert.match(go, /shareEventKindView\s*=\s*"view"/);
  assert.match(go, /shareEventKindProgress\s*=\s*"progress"/);
  assert.match(go, /"progress_" \+ itoaShareBucket/);
});

test('播放器确实接线：首帧、逐帧、拖动、播完', () => {
  const main = read('./src/sharePlayer/main.ts');
  assert.match(main, /const telemetry = startShareTelemetry\(\)/);
  assert.match(main, /telemetry\?\.reportView\(\)/);
  assert.match(main, /telemetry\?\.tick\(progress\(us, durationUs\)\)/);
  assert.match(main, /telemetry\?\.seek\(progress\(target, durationUs\)\)/);
  assert.match(main, /telemetry\?\.finish\(\)/);
  // 不启用时必须直接返回 null，绝不能「先建 fetch 再判断」。
  assert.match(main, /if \(!shouldReportTelemetry\(env\)\) return null/);
  // 网络副作用只能出现在 startShareTelemetry 内部。
  const fetchCount = (main.match(/\bfetch\(/g) || []).length;
  assert.equal(fetchCount, 1, '播放器里只允许一处埋点 fetch');
  assert.match(main, /keepalive: true/);
  assert.match(main, /'Content-Type': 'application\/json'/);
  assert.match(main, /\/\/ fetch-ok:/, '站内 API 契约要求逐处标注理由');

  // 白名单必须是「一个文件、一处 fetch」，不能被悄悄扩成整个目录。
  const contract = read('./api-contract-check.mjs');
  assert.match(contract, /SHARE_PLAYER_FETCH_FILES = new Set\(\['sharePlayer\/main\.ts'\]\)/);
  assert.match(contract, /sharePlayerFetch && fetchLines\.length !== 1/);
});

test('CSP 同源许可与零外链边界', () => {
  const html = read('../internal/share/html.go');
  assert.match(html, /connect-src 'self'/);
  // 不能为了埋点放宽到任意远程来源。
  assert.doesNotMatch(html, /connect-src[^"']*\*/);
  assert.doesNotMatch(html, /connect-src[^"']*https:/);
  const serve = read('../internal/api/artifact_share_serve.go');
  assert.match(serve, /POST \/s\/\{token\}\/events/);
  assert.match(serve, /connect-src 'self'/);
  const selfcontain = read('../internal/share/selfcontain.go');
  // V14 判据必须原样保留（外部引用数为 0），不能靠放宽扫描让埋点过。
  assert.match(selfcontain, /\[\]string\{"http:\/\/", "https:\/\/", "\/\/", "file:"\}/);
  assert.match(selfcontain, /return strings\.HasPrefix\(r, "\/"\)/);
});

test('编号订正：CI 与门禁不再把 V18 当埋点（只锚定随仓库分发的文件）', () => {
  // 设计文档在 docs/ 下，按 tasks 6.4 明确移出仓库（.gitignore `/docs/*`，只留 adr/）。
  // 曾经有一条判据直接 read('../docs/动画视频扩展-设计方案-V1.0.md')：本地绿，
  // CI 干净检出里没有这个文件 → ENOENT → telemetry:test 整组红。
  // 编号订正的判据必须锚定随代码走、且不受 openspec archive 移动的文件。
  const ci = read('../.github/workflows/ci.yml');
  assert.match(ci, /页缝延续门禁（V19）/);
  assert.match(ci, /切图门禁（V18）/);
  assert.match(ci, /埋点完整性门禁（V20）/);
  assert.match(ci, /编号刻意跳过 V18\/V19/);
  assert.doesNotMatch(ci, /V18 埋点/);
  const gate = read('../scripts/review/telemetry-gate.mjs');
  assert.match(gate, /V20 埋点完整性门禁/);
  assert.match(gate, /V20\.\d/, '门禁判据码必须带 V20 前缀');
  const inventory = read('./route-inventory.json');
  assert.match(inventory, /V20 新增 POST \/s\/\{token\}\/events/);
  // 仓库外的方案文档若存在，本地再顺手核一次；不存在不算失败。
  const docPath = new URL('../docs/动画视频扩展-设计方案-V1.0.md', import.meta.url);
  if (existsSync(docPath)) {
    const doc = readFileSync(docPath, 'utf8');
    assert.match(doc, /### 20\.8 质量门禁扩展（V13–V20）/);
    assert.match(doc, /\| V20 \| \*\*埋点完整性\*\*/);
    assert.match(doc, /\| V18 \| \*\*切图门禁\*\*/);
    assert.match(doc, /\| V19 \| \*\*页缝延续门禁\*\*/);
    assert.doesNotMatch(doc, /V18 埋点/);
  }
});
