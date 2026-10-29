// 单页降级：把一页「冻结」成静态画面（tasks 3.7）。
//
// 为什么不是「删掉这一页」：片段数量与页序是服务契约的一部分，少一段要靠上游补，
// 而补出来的静态段与这里能出的画面本就是同一张图。由渲染器自己出静态段，
// 产物自洽（artifacts 齐全、时长准确），任何调用方都能直接拼接。
//
// 冻结的做法不是「删掉 clips」而是「把每条 clip 压成零长」：
//   clipProgress 对 durationMs <= 0 的行为是「起点之后恒为 1」，
//   于是任意时刻取到的都是 to 值 —— 即该页末帧的元素状态。
// 删 clips 会让 evaluateLayer 退回恒等（opacity=1、无位移），
// 对 fadeOut 的出场元素就会「该消失的还留着」，与静态段应有的画面不一致。

// 与 Go 侧 internal/render/service.go 的 DegradationStill / DegradationSkipped 一一对应。
// 两边各自是常量，值一旦漂移，「哪一页为什么降级」就会在上游失配（那里按 code 分支）。
// degrade-check.mjs 钉住了这两个字面量，改值时两侧会同时红。
export const DEGRADED_STILL = 'page_render_failed';
export const DEGRADED_SKIPPED = 'page_skipped';

/** 把一页的动效压成终态，保留图层与转场。不修改入参。 */
export function stillPage(page) {
  const layers = (page.layers ?? []).map((layer) => ({
    ...layer,
    clips: (layer.clips ?? []).map((clip) => ({ ...clip, startMs: 0, durationMs: 0 }))
  }));
  return { ...page, layers };
}

/** 只冻结第 index 页，其余页原样引用（渲染同一份 plan 的其他页不应受影响）。 */
export function stillTimeline(timeline, index) {
  const pages = (timeline.pages ?? []).map((page, i) => (i === index ? stillPage(page) : page));
  return { ...timeline, pages };
}

/**
 * 解析故障注入清单（PPTS_RENDER_FAULT_PAGES="1,3"）。
 *
 * 这个开关只为验收降级路径存在：真实渲染里让「某一页必然失败」没有可靠的造法
 * （页面内容合法就不会失败），而这条路径一旦回归，用户看到的只是「这页没动画」，
 * 不会报错。非法项直接丢弃——注入开关本身不该让渲染起不来。
 */
export function faultPages(spec) {
  const out = new Set();
  for (const raw of String(spec ?? '').split(',')) {
    const n = Number(raw.trim());
    if (raw.trim() !== '' && Number.isInteger(n) && n >= 0) out.add(n);
  }
  return out;
}
