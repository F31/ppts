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
//   {"type":"page","page":0,"file":"segment_000.mp4","frames":180}
//   {"type":"done","segments":5,"frames":900}
// Go 侧按行解析，不依赖任何终端控制字符。

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { bundle } from '@remotion/bundler';
import { renderMedia, selectComposition } from '@remotion/renderer';

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
  const out = { plan: '', outDir: '', concurrency: 2, crf: 18, gop: 30 };
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
for (let i = 0; i < timeline.pages.length; i++) {
  const total = framesPerPage(timeline.pages[i].durationMs);
  const from = starts[i];
  const to = from + total - 1;
  const file = `segment_${String(i).padStart(3, '0')}.mp4`;

  try {
    await renderMedia({
      ...common,
      outputLocation: path.join(outDir, file),
      frameRange: [from, to],
      onProgress: ({ renderedFrames }) => {
        emit({ type: 'progress', page: i, done: renderedFrames, total });
      }
    });
  } catch (err) {
    fail(`page ${i} (frameRange ${from}-${to}) failed: ${err.message}`);
  }

  emit({ type: 'page', page: i, file, frames: total });
  renderedSegments++;
}

emit({ type: 'done', segments: renderedSegments, frames: cursor });
