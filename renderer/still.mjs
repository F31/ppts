#!/usr/bin/env node
// 渲染单帧为 PNG：排查「某一刻到底画了什么」。
//
// 用途：残影、转场过冲、字幕对齐这类问题，看一段视频不如看一帧。
// 用法：node still.mjs --plan <plan.json> --frame 10 --out frame.png

import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { bundle } from '@remotion/bundler';
import { renderStill, selectComposition } from '@remotion/renderer';
import fs from 'node:fs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

function parseArgs(argv) {
  const out = { plan: '', frame: 0, out: 'frame.png' };
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--plan':
        out.plan = argv[++i];
        break;
      case '--frame':
        out.frame = Number(argv[++i]);
        break;
      case '--out':
        out.out = argv[++i];
        break;
      default:
        break;
    }
  }
  return out;
}

const args = parseArgs(process.argv.slice(2));
if (!args.plan) {
  console.error('--plan is required');
  process.exit(1);
}

const planPath = path.resolve(args.plan);
const timeline = JSON.parse(fs.readFileSync(planPath, 'utf8'));
const browserExecutable = process.env.PPTS_CHROME || undefined;

const serveUrl = await bundle({
  entryPoint: path.join(__dirname, 'src', 'index.tsx'),
  publicDir: path.dirname(planPath),
  onProgress: () => undefined
});

const composition = await selectComposition({
  serveUrl,
  id: 'Timeline',
  inputProps: timeline,
  browserExecutable
});

await renderStill({
  serveUrl,
  composition,
  inputProps: timeline,
  output: path.resolve(args.out),
  frame: args.frame,
  overwrite: true,
  browserExecutable,
  imageFormat: 'png'
});

console.log(`wrote ${args.out} (frame ${args.frame})`);
