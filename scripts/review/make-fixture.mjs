#!/usr/bin/env node
// 造一份可复现的「成片夹具」：成品包 + 时间轴 + 逐页 MP4 分段。
//
// 为什么需要它：V13（dom-ssim.py，包端 vs 成片）一直只能对着真实作业跑，而真实作业
// 要渲染镜像才有 MP4。于是这条门禁长期「本地跑不起来、改了也无法验证」——上一轮把
// 截图半边换成原生 Chrome 时就是因为这个原因没敢动它。
//
// 夹具的做法不是「假装有渲染」：MP4 的每一帧都取自**成品包自身的 DOM**，按 30fps
// 逐帧 seek 截出来再编码。所以它能证明的事很明确：
//   * 截图半边（seek 落点、舞台裁剪、解码抽帧）是通的；
//   * 判据的分母是真的——编码损失的 SSIM 天花板就在 0.998 量级；
//   * 坏料会被抓住（见 --shift 造的负例）。
// 它不能证明的事同样明确：包与**真实渲染**是否一致。那个只有成片能答，判据不变，
// 只是现在不再需要 docker 才能跑起来。
//
// 用法：
//   node scripts/review/make-fixture.mjs --out tmp/v13fix
//   node scripts/review/make-fixture.mjs --out tmp/v13fix --pages 2 --page-secs 2
// 产物：
//   <out>/pkg/            成品包
//   <out>/pages/pNNN.png  源页图
//   <out>/plan.json       时间轴（从清单派生，供 dom-ssim.py 取取样点）
//   <out>/segment_NNN.mp4 逐页分段
// 之后：
//   python3 scripts/review/dom-ssim.py --plan <out>/plan.json --pkg <out>/pkg \
//       --segments <out> --out <out>/gate
import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const FPS = 30;
const CRF = 18; // 与渲染侧同档：门禁比的是这一档编码损失之下的画面一致性

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const outDir = path.resolve(arg('out', 'tmp/v13fix'));
const pageCount = Number(arg('pages', '3'));
const pageSecs = Number(arg('page-secs', '2'));
const width = Number(arg('width', '1280'));
const height = Number(arg('height', '720'));

// ---- ffmpeg 的两种存在方式（Windows 上通常只在 WSL 里）----
let ffmpegVia = 'native';
if (!which('ffmpeg')) {
  if (process.platform === 'win32' && which('wsl')) {
    try {
      execFileSync('wsl', ['-e', 'bash', '-lc', 'command -v ffmpeg'], { stdio: 'ignore' });
      ffmpegVia = 'wsl';
    } catch {
      throw new Error('找不到 ffmpeg（本机与 WSL 都没有）：夹具需要它编码分段');
    }
  } else {
    throw new Error('找不到 ffmpeg：夹具需要它编码分段');
  }
}

function which(cmd) {
  try {
    execFileSync(process.platform === 'win32' ? 'where' : 'which', [cmd], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

function toWslPath(p) {
  const abs = path.resolve(p);
  return execFileSync('wsl', ['-e', 'wslpath', '-a', '-u', abs], { encoding: 'utf8' }).trim();
}

function ffmpeg(args, { quiet = true } = {}) {
  if (ffmpegVia === 'native') {
    execFileSync('ffmpeg', args, { stdio: quiet ? 'ignore' : 'inherit' });
    return;
  }
  const quoted = args.map((a) => `'${String(a).replace(/'/g, `'\\''`)}'`).join(' ');
  execFileSync('wsl', ['-e', 'bash', '-lc', `ffmpeg ${quoted}`], {
    stdio: quiet ? 'ignore' : 'inherit',
  });
}

function sh(cmd, args, opts = {}) {
  return execFileSync(cmd, args, { encoding: 'utf8', ...opts });
}

// 调 dom-shot 的 CLI 而不是 import：它把结果写成一行 SHOT_JSON，这是它与其他
// 调用方（domshot.py）之间唯一的约定，两个语言各解析一次比维护两套截图实现便宜。
function shoot(pkg, times, out, stage) {
  const args = [
    path.join(HERE, 'dom-shot.mjs'),
    '--pkg', pkg, '--out', out,
    '--times', times.join(','),
    '--stage', `${stage[0]}x${stage[1]}`,
  ];
  const proc = spawnSync(process.execPath, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 });
  const line = (proc.stdout || '').split('\n').find((l) => l.startsWith('SHOT_JSON '));
  if (!line) {
    const detail = (proc.stderr || proc.stdout || '').trim().slice(0, 400);
    throw new Error(`dom-shot 没有输出结果（exit ${proc.status}）：${detail || '无输出'}`);
  }
  const report = JSON.parse(line.slice('SHOT_JSON '.length));
  if (!report.ok) throw new Error(`截图失败：${report.error}`);
  return report;
}

async function main() {
  fs.rmSync(outDir, { recursive: true, force: true });
  fs.mkdirSync(outDir, { recursive: true });

  // 1) 成品包：走真实打包器，不手搓清单——夹具一旦和产物不是同一条路径，
  //    它守的东西就与线上无关了。
  console.log(`[fixture] 打包 ${pageCount} 页 × ${pageSecs}s @ ${width}×${height}`);
  try {
    sh('go', ['run', './cmd/share-baseline',
      '-pages', String(pageCount), '-page-secs', String(pageSecs),
      '-layers', '2', '-width', String(width), '-height', String(height),
      '-pages-out', path.join(outDir, 'pages'), '-out', path.join(outDir, 'pkg')],
    { stdio: ['ignore', 'pipe', 'pipe'] });
  } catch (err) {
    // 打包器的失败原因都在 stderr 里；吞掉它只会得到一句「go run 退出 1」。
    const detail = String(err.stderr || err.stdout || '').trim().slice(0, 500);
    throw new Error(`share-baseline 失败：${detail || err.message}`);
  }

  const manifest = JSON.parse(fs.readFileSync(path.join(outDir, 'pkg', 'manifest.json'), 'utf8'));
  const slides = manifest.slides || [];
  if (slides.length !== pageCount) {
    throw new Error(`清单有 ${slides.length} 页，期望 ${pageCount} 页`);
  }

  // 2) 时间轴：取样点（首帧 / 第一段 clip 中点 / 末帧前一帧）都从这里来。
  const plan = {
    schemaVersion: 1,
    fps: FPS,
    width: manifest.video.width,
    height: manifest.video.height,
    pages: slides.map((s) => ({
      durationMs: Math.round((Number(s.endUs) - Number(s.startUs)) / 1000),
      layers: (s.layers || []).map((l) => ({
        elementId: l.elementId || '',
        clips: (l.clips || []).map((c) => ({
          startMs: c.startMs, durationMs: c.durationMs, props: c.props || [],
        })),
      })),
    })),
  };
  fs.writeFileSync(path.join(outDir, 'plan.json'), JSON.stringify(plan, null, 2) + '\n');

  // 3) 逐帧截 DOM：整段一次调用（一个浏览器会话跑完），按 30fps 的时间网格。
  const totalMs = plan.pages.reduce((sum, p) => sum + p.durationMs, 0);
  const totalFrames = Math.round((totalMs / 1000) * FPS);
  const times = Array.from({ length: totalFrames }, (_, k) => Math.round((k * 1000) / FPS));
  console.log(`[fixture] 截 ${totalFrames} 帧（${totalMs}ms @ ${FPS}fps）`);
  const report = shoot(
    path.join(outDir, 'pkg'), times, path.join(outDir, 'shots'), [plan.width, plan.height]
  );
  const stage = report.shots[0]?.stage;
  if (!stage || report.shots.some((s) => !s.ok || !s.stage)) {
    throw new Error('有帧没截成或有图没解码完：夹具不可用');
  }
  for (const s of report.shots) {
    if (s.stage.x !== stage.x || s.stage.y !== stage.y) {
      throw new Error(`舞台位置在截图中途变了：${JSON.stringify(s.stage)} vs ${JSON.stringify(stage)}`);
    }
  }

  // 4) 编码：每页一段，裁剪到舞台（成片里没有播放器外壳，只有画面）。
  const perPage = Math.round(pageSecs * FPS);
  let offset = 0;
  for (let i = 0; i < pageCount; i++) {
    const dir = path.join(outDir, 'frames', `p${String(i).padStart(3, '0')}`);
    fs.mkdirSync(dir, { recursive: true });
    for (let k = 0; k < perPage; k++) {
      const shot = report.shots[offset + k];
      if (!shot) throw new Error(`第 ${i} 页缺第 ${k} 帧`);
      fs.copyFileSync(shot.file, path.join(dir, `f${String(k).padStart(5, '0')}.png`));
    }
    offset += perPage;

    const src = path.join(dir, 'f%05d.png');
    const dst = path.join(outDir, `segment_${String(i).padStart(3, '0')}.mp4`);
    const args = [
      '-hide_banner', '-loglevel', 'error', '-y',
      '-framerate', String(FPS), '-i', ffmpegVia === 'wsl' ? toWslPath(src) : src,
      '-vf', `crop=${stage.w}:${stage.h}:${stage.x}:${stage.y}`,
      '-c:v', 'libx264', '-preset', 'veryfast', '-crf', String(CRF),
      '-pix_fmt', 'yuv420p', '-movflags', '+faststart',
      ffmpegVia === 'wsl' ? toWslPath(dst) : dst,
    ];
    ffmpeg(args);
    const size = fs.statSync(dst).size;
    console.log(`[fixture] segment_${String(i).padStart(3, '0')}.mp4  ${(size / 1024).toFixed(0)}KB`);
  }
  if (offset !== totalFrames) {
    throw new Error(`帧数对不上：用了 ${offset} 帧，截了 ${totalFrames} 帧`);
  }

  fs.writeFileSync(path.join(outDir, 'fixture.json'), JSON.stringify({
    createdAt: new Date().toISOString(),
    ffmpeg: ffmpegVia,
    browser: report.chrome,
    fps: FPS,
    crf: CRF,
    stage: { w: stage.w, h: stage.h },
    pages: pageCount,
    pageSecs,
    frames: totalFrames,
  }, null, 2) + '\n');

  console.log(`[fixture] 完成：${outDir}`);
  console.log(`  python3 scripts/review/dom-ssim.py --plan ${path.relative(process.cwd(), path.join(outDir, 'plan.json'))} `
    + `--pkg ${path.relative(process.cwd(), path.join(outDir, 'pkg'))} `
    + `--segments ${path.relative(process.cwd(), outDir)} --out ${path.relative(process.cwd(), path.join(outDir, 'gate'))}`);
}

main().then(
  () => process.exit(0),
  (err) => {
    console.error(String((err && err.message) || err));
    process.exit(1);
  }
);
