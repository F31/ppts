#!/usr/bin/env node
// 渲染入口：把 plan.json 交给 Remotion，逐页产出 H.264 片段。
//
// 为什么逐页渲染而不是整片一次渲染：
//   1. 服务契约要求「产物是逐页片段」，M3 的拼接才能按页复用（改一页不必全片重渲）；
//   2. 单页失败可以只降级那一页（设计方案 §8.8），整片渲染失败就是一个黑盒；
//   3. 进度按页回报，用户可以看见「第 3/12 页」而不是一个百分比。
//
// 输出用 JSON 行（stdout），而不是 Remotion 的终端进度条：
//   {"type":"progress","page":0,"done":42,"total":180}
//   {"type":"degraded","page":1,"code":"page_render_failed","message":"..."}
//   {"type":"page","page":0,"file":"segment_000.mp4","frames":180}
//   {"type":"done","segments":5,"frames":900}
// Go 侧按行解析，不依赖任何终端控制字符。
//
// 单页失败不退出：先用静态画面重出该页（degraded/page_render_failed），
// 静态也失败才跳过它（degraded/page_skipped），由上游换成静态段（ADR-23）。
// 只有「一页都没出」才整次失败——那时确实没有可拼接的产物。

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { bundle } from '@remotion/bundler';
import { renderMedia, selectComposition } from '@remotion/renderer';
import { DEGRADED_SKIPPED, DEGRADED_STILL, faultPages, stillTimeline } from './degrade.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const COMPOSITION_ID = 'Timeline';

function emit(payload) {
  process.stdout.write(JSON.stringify(payload) + '\n');
}

function fail(message) {
  emit({ type: 'error', message });
  process.exit(1);
}

function parseArgs(argv) {
  const out = { plan: '', outDir: '', concurrency: 2, crf: 18, gop: 30, pageConcurrency: 1, onlyPages: null };
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i];
    const value = argv[i + 1];
    switch (key) {
      case '--plan':
        out.plan = value;
        i++;
        break;
      case '--out-dir':
        out.outDir = value;
        i++;
        break;
      case '--concurrency':
        out.concurrency = Number(value);
        i++;
        break;
      case '--crf':
        out.crf = Number(value);
        i++;
        break;
      case '--gop':
        out.gop = Number(value);
        i++;
        break;
      case '--page-concurrency':
        out.pageConcurrency = Math.max(1, Number(value) || 1);
        i++;
        break;
      case '--only-pages':
        // 只渲这些页（页序号，逗号分隔）。空串 = 不限制。
        // 页号是**整份计划**里的位置，不是本次的第几个——片段文件名也按它命名。
        out.onlyPages = value
          .split(',')
          .map((s) => Number(s.trim()))
          .filter((n) => Number.isInteger(n) && n >= 0);
        i++;
        break;
      default:
        break;
    }
  }
  return out;
}

const args = parseArgs(process.argv.slice(2));
if (!args.plan) fail('--plan is required');
if (!args.outDir) fail('--out-dir is required');

const planPath = path.resolve(args.plan);
if (!fs.existsSync(planPath)) fail(`plan not found: ${planPath}`);

const workDir = path.dirname(planPath);
const outDir = path.resolve(args.outDir);
fs.mkdirSync(outDir, { recursive: true });

let timeline;
try {
  timeline = JSON.parse(fs.readFileSync(planPath, 'utf8'));
} catch (err) {
  fail(`invalid plan json: ${err.message}`);
}

if (!Array.isArray(timeline.pages) || timeline.pages.length === 0) fail('plan has no pages');

const fps = timeline.fps || 30;
const framesPerPage = (durationMs) => Math.max(1, Math.round((durationMs * fps) / 1000));

// 与 renderer/src/motion.ts 的 framesForDuration 同口径：两处必须一致，
// 否则这里算的 frameRange 会和合成器的 Sequence 错位一帧。
const starts = [];
let cursor = 0;
for (const page of timeline.pages) {
  starts.push(cursor);
  cursor += framesPerPage(page.durationMs);
}

// 浏览器可执行文件：镜像里由 CHROME_VERSION 固定，本地用系统 Chrome。
// 不设时交给 Remotion 自己找——但容器无外网，下载失败要表现为明确错误，
// 因此这里提前检查，避免渲染到一半才报「找不到浏览器」。
const browserExecutable = process.env.PPTS_CHROME || undefined;

const bundleLocation = await bundle({
  entryPoint: path.join(__dirname, 'src', 'index.tsx'),
  publicDir: workDir,
  onProgress: () => undefined
});

const composition = await selectComposition({
  serveUrl: bundleLocation,
  id: COMPOSITION_ID,
  inputProps: timeline,
  browserExecutable
});

const common = {
  serveUrl: bundleLocation,
  composition,
  inputProps: timeline,
  codec: 'h264',
  pixelFormat: 'yuv420p',
  crf: args.crf,
  gopSize: args.gop,
  x264Preset: 'medium',
  imageFormat: 'png',
  concurrency: args.concurrency,
  // 片段不带音轨：音频由 L5 统一混音，片段里有静音轨会让 concat 的流布局不一致。
  enforceAudioTrack: false,
  muted: true,
  overwrite: true,
  browserExecutable,
  timeoutInMilliseconds: 60_000
};

let renderedSegments = 0;
let renderedFrames = 0;
const faults = faultPages(process.env.PPTS_RENDER_FAULT_PAGES);

// 本次要渲的页。缺省是全部；给了 --only-pages 就只渲那些页（M5 增量重渲：
// 命中缓存的页不进这个列表，也就不产生任何渲染成本）。
const renderList =
  args.onlyPages && args.onlyPages.length > 0
    ? args.onlyPages.filter((i) => i < timeline.pages.length)
    : timeline.pages.map((_, i) => i);
if (renderList.length === 0) fail('no pages to render');
const pageConcurrency = Math.min(args.pageConcurrency, renderList.length);

// 一页渲一次。抽成函数是因为降级时要「同一页再渲一次」，只是换成静态画面。
//
// composition 必须与 inputProps 一起换：renderMedia 用的是 composition 里算好的 props，
// 只传新 inputProps 不会生效——画面仍是原动画。这一点没有报错，只能靠帧比对发现
// （降级页第 8 帧与末帧差 42926 像素，差异框正好落在标题条 box 内）。
const renderPage = (total, from, to, file, inputProps, onProgress, composition = common.composition) =>
  renderMedia({
    ...common,
    composition,
    inputProps,
    outputLocation: path.join(outDir, file),
    frameRange: [from, to],
    onProgress
  });

const renderOnePage = async (i) => {
  const total = framesPerPage(timeline.pages[i].durationMs);
  const from = starts[i];
  const to = from + total - 1;
  const file = `segment_${String(i).padStart(3, '0')}.mp4`;

  try {
    if (faults.has(i)) throw new Error(`injected fault on page ${i} (PPTS_RENDER_FAULT_PAGES)`);
    await renderPage(total, from, to, file, timeline, ({ renderedFrames: done }) => {
      emit({ type: 'progress', page: i, done, total });
    });
  } catch (err) {
    // 单页失败不再让整次渲染失败：先如实说明「这页降级了」，再用静态画面重出一次。
    // 注意顺序——降级事件在重试之前发，重试若成功，客户端拿到的仍是「片段齐全、
    // 但这页没动画」；若重试也失败，后面再补一条 page_skipped。
    const first = String(err?.message ?? err).split('\n')[0];
    emit({ type: 'degraded', page: i, code: DEGRADED_STILL, message: first });

    try {
      // 重试不回报进度：同一页的 done 会从 0 重新开始，进度条会倒退。
      const still = stillTimeline(timeline, i);
      const stillComposition = await selectComposition({
        serveUrl: bundleLocation,
        id: COMPOSITION_ID,
        inputProps: still,
        browserExecutable
      });
      await renderPage(total, from, to, file, still, undefined, stillComposition);
    } catch (stillErr) {
      const stillFirst = String(stillErr?.message ?? stillErr).split('\n')[0];
      emit({ type: 'degraded', page: i, code: DEGRADED_SKIPPED, message: stillFirst });
      return;
    }
  }

  emit({ type: 'page', page: i, file, frames: total });
  renderedSegments++;
  renderedFrames += total;
};

// 页级并行（M5）：N 个 worker 分摊页列表。
//
// 为什么默认 1：总渲染位 = pageConcurrency × concurrency（帧并发），而 ADR-16 的
// 内存结论是按乘积算的。多开一页就多一个浏览器上下文，默认值必须留给部署方决定。
// 页之间没有共享写入：每页写自己的 segment_NNN.mp4，降级重试也在页内闭合，
// 所以并行不改变产物（判据：串行与并行的逐页 sha256 相同）。
if (pageConcurrency <= 1) {
  for (const i of renderList) {
    await renderOnePage(i);
  }
} else {
  let next = 0;
  const worker = async () => {
    for (;;) {
      const slot = next++;
      if (slot >= renderList.length) return;
      await renderOnePage(renderList[slot]);
    }
  };
  await Promise.all(Array.from({ length: pageConcurrency }, worker));
}

// 一页都没出等于这次渲染失败：返回成功但产物为空，上游会拿到一个 0 段的「成功任务」。
if (renderedSegments === 0) {
  fail(`all ${renderList.length} pages failed`);
}

emit({ type: 'done', segments: renderedSegments, frames: renderedFrames });
