#!/usr/bin/env node
// V19 页缝延续门禁：跨页元素在页边界处必须「接得上」。
//
// 为什么单独一条：V13b 看「每页末帧 == 参照页图」，判据是终态；而延续元素在页尾本来就是
// **中途态**（这正是它跨页的意义），所以它天然不能进 V13b。V17 看降级形态，也不判连续性。
// 结果就是「延续段的链接字段写错 / offset 写反 / 源段与延续段 Props 不一致」这三类回归，
// 在既有门禁里全都看不见——它们在成片里表现为页缝处元素闪一下或跳回起点。
//
// 判据取在**成品包清单 + 共享求值器**上，而不是像素：
//   * 用的是真正打进包里的 manifest.json，编译/打包任何一环断了都会露出来；
//   * 求值器与播放器、渲染器同一份（shared/motion.ts），不存在「门禁绿但两端错」的分歧；
//   * 不需要浏览器，CI 里可以和浏览器作业并行，也不会因截图抖动而假红。
// 阈值 1e-6 不是「宽容度」：负起点平移下两侧的 raw 进度在数学上完全相同，理应逐位相等；
// 留 1e-6 只为吸收浮点乘加的结合顺序差异。
//
// 用法：
//   node --experimental-strip-types scripts/review/seam-gate.mjs --pkg /tmp/pkg-cont
//   node --experimental-strip-types scripts/review/seam-gate.mjs --pkg DIR --json
//
// 退出码：0 全绿；1 有判据失败（含「一条延续段都没有」——夹具坏了不许静默通过）。

import fs from 'node:fs';
import path from 'node:path';

import { evaluateLayer } from '../../shared/motion.ts';

const TOL = 1e-6;

function parseArgs(argv) {
  const out = { pkg: '', json: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--pkg') out.pkg = argv[++i] ?? '';
    else if (a === '--json') out.json = true;
    else if (a.startsWith('--pkg=')) out.pkg = a.slice('--pkg='.length);
    else {
      console.error(`seam-gate: 未知参数 ${a}`);
      process.exit(2);
    }
  }
  if (!out.pkg) {
    console.error('seam-gate: 必须给 --pkg <成品包目录>');
    process.exit(2);
  }
  return out;
}

const cases = [];
function check(code, ok, detail) {
  cases.push({ code, ok: Boolean(ok), detail });
}

/** 页时长（毫秒）。清单只有 startUs/endUs，页内时间全部以毫秒表达（见 render-plan）。 */
function pageMsOf(slide) {
  return (slide.endUs - slide.startUs) / 1000;
}

function layerClips(layer) {
  return Array.isArray(layer.clips) ? layer.clips : [];
}

/** Props 是 [{name,from,to}]：顺序无意义（求值器按属性逐条累加，与顺序无关），比较前归一。 */
function propsKey(clip) {
  const props = Array.isArray(clip.props) ? clip.props : [];
  return JSON.stringify(
    [...props]
      .map((p) => [p.name, Number(p.from), Number(p.to)])
      .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
  );
}

function near(a, b) {
  return Math.abs(a - b) <= TOL;
}

function stateDelta(a, b) {
  return {
    opacity: Math.abs(a.opacity - b.opacity),
    tx: Math.abs(a.tx - b.tx),
    ty: Math.abs(a.ty - b.ty),
    sx: Math.abs(a.sx - b.sx),
    sy: Math.abs(a.sy - b.sy),
  };
}

function maxDelta(d) {
  return Math.max(d.opacity, d.tx, d.ty, d.sx, d.sy);
}

function fmtDelta(d) {
  return `opacity=${d.opacity.toExponential(2)} tx=${d.tx.toExponential(2)} ty=${d.ty.toExponential(2)} ` +
    `sx=${d.sx.toExponential(2)} sy=${d.sy.toExponential(2)}`;
}

const args = parseArgs(process.argv.slice(2));
const manifestPath = path.join(args.pkg, 'manifest.json');
if (!fs.existsSync(manifestPath)) {
  console.error(`seam-gate: 找不到 ${manifestPath}（需要一个已解包/已写盘的成品包目录）`);
  process.exit(2);
}
const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
const slides = Array.isArray(manifest.slides) ? manifest.slides : [];
check('V19.0 清单至少有 2 页', slides.length >= 2, `${slides.length} 页`);

const pageMs = slides.map(pageMsOf);
let continuationCount = 0;

for (let i = 0; i < slides.length; i++) {
  const slide = slides[i];
  const layers = Array.isArray(slide.layers) ? slide.layers : [];
  for (let k = 0; k < layers.length; k++) {
    const layer = layers[k];
    for (let c = 0; c < layerClips(layer).length; c++) {
      const clip = layerClips(layer)[c];
      if (!clip.isContinuation) continue;
      continuationCount++;
      const where = `slide ${i + 1} layer ${k} clip ${c}（${layer.elementId || '无 elementId'}）`;

      // 1) 链接字段：延续段必须自带 elementId，且与所在层的 elementId 一致。
      //    缺了它，播放器/渲染器无从把次页层与前一页的元素认成同一个实体。
      const clipId = clip.elementId ?? '';
      check('V19.1 延续段带 elementId 且与本层一致',
        Boolean(clipId) && clipId === (layer.elementId ?? ''),
        `${where} clip.elementId=${JSON.stringify(clip.elementId)} layer.elementId=${JSON.stringify(layer.elementId)}`);

      // 2) 时间关系：startMs = -continuationOffsetMs。写成正起点等于「次页从头重播」，
      //    正是这条门禁要拦的回归。
      const offset = Number(clip.continuationOffsetMs ?? -1);
      check('V19.2 延续段 startMs = -continuationOffsetMs 且 offset 非负',
        offset >= 0 && Number(clip.startMs) === -offset,
        `${where} startMs=${clip.startMs} offset=${clip.continuationOffsetMs}`);

      // 3) 不允许出现在第 1 页：没有「前一页」就没有延续的来源，说明夹具/编译错了。
      check('V19.3 延续段不在首屏', i > 0, `${where} 出现在第 ${i + 1} 页`);

      const prev = slides[i - 1];
      const prevMs = pageMs[i - 1];
      const prevLayers = prev && Array.isArray(prev.layers) ? prev.layers : [];
      const sameId = prevLayers.filter((l) => (l.elementId ?? '') === clipId && clipId !== '');

      // 4) 前一页必须有同 elementId 的源层，且源段与延续段是「同一个 clip 的两截」：
      //    同 effect、同 duration、同 Props，且源段起点 = 前一页页长 - offset。
      const wantStart = prevMs - offset;
      let srcLayer = null;
      let srcClip = null;
      for (const l of sameId) {
        for (const sc of layerClips(l)) {
          if (sc.isContinuation) continue;
          if (Number(sc.startMs) !== wantStart) continue;
          if (Number(sc.durationMs) !== Number(clip.durationMs)) continue;
          if ((sc.effect ?? '') !== (clip.effect ?? '')) continue;
          if (propsKey(sc) !== propsKey(clip)) continue;
          srcLayer = l;
          srcClip = sc;
          break;
        }
        if (srcClip) break;
      }
      check('V19.4 前一页存在配对的源段（同 elementId / effect / duration / Props）',
        Boolean(srcClip),
        srcClip
          ? `${where} ← 源段 startMs=${srcClip.startMs} durationMs=${srcClip.durationMs} effect=${srcClip.effect}`
          : `${where} 未找到源段（期望 startMs=${wantStart} durationMs=${clip.durationMs}；前一页同 elementId 层 ${sameId.length} 个）`);

      // 5) 页缝求值连续：前一页页尾 vs 次页 0 点，同一元素的合成态必须一致。
      //    这是「不闪、不跳回起点」的数学定义；两侧 raw 进度同为 offset/duration。
      if (srcClip) {
        const before = evaluateLayer(srcLayer, prevMs);
        const after = evaluateLayer(layer, 0);
        const d = stateDelta(before, after);
        check('V19.5 页缝处求值连续（opacity / 位移 / 缩放）',
          maxDelta(d) <= TOL,
          `${where} Δ ${fmtDelta(d)}`);
        check('V19.6 页缝处元素处于中途态（不是已定格，也不是未开始）',
          // 若两侧都恰好是终态/初态，说明这条 clip 根本没跨过页缝，夹具形同虚设。
          clipProgressOf(clip, 0) > 0 && clipProgressOf(clip, 0) < 1,
          `${where} 次页 localMs=0 的进度=${clipProgressOf(clip, 0).toFixed(4)}`);
      }
    }
  }
}

/** 单条 clip 在页内某时刻的线性进度（只看有没有跨页缝，不参与连续性判据）。 */
function clipProgressOf(clip, localMs) {
  const d = Number(clip.durationMs);
  if (!(d > 0)) return 0;
  const raw = (localMs - Number(clip.startMs)) / d;
  return raw <= 0 ? 0 : raw >= 1 ? 1 : raw;
}

// 6) 空跑必须红：夹具没产出延续段时，上面所有判据都会「因为没东西可查」而全绿，
//    那是门禁最危险的形态（看起来在守，其实什么都没守）。
check('V19.7 包里确实存在延续段', continuationCount > 0, `${continuationCount} 条`);

const failed = cases.filter((c) => !c.ok);
if (args.json) {
  console.log(JSON.stringify({ pkg: args.pkg, ok: failed.length === 0, cases }, null, 2));
} else {
  console.log(`[V19] 页缝延续门禁：${args.pkg}`);
  for (const c of cases) {
    console.log(`  ${c.ok ? 'ok  ' : 'FAIL'} ${c.code}  ${c.detail}`);
  }
  console.log(`[V19] ${cases.length - failed.length}/${cases.length} 通过`);
}
process.exit(failed.length === 0 ? 0 : 1);
