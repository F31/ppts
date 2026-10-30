// 动画 IR 的共享类型。
//
// 放在仓库根 `shared/` 而不是某一侧：renderer（Remotion/Node）与 web（分享播放器）
// 必须用**同一份**定义与**同一份**求值代码。两端各写一份的典型后果是
// 「浏览器预览里入场是 600ms，导出的 mp4 里是 400ms」——这是客户最容易发现、
// 也最伤信任的一类 bug（设计方案 §20.4）。
//
// 字段与 `internal/render/ir.go` 逐一对齐：JSON 名、可空性、单位（像素/毫秒）。
// 这里刻意不做运行时校验：校验权威在 Go 侧（Validate 已在提交前跑过），
// 前端重复校验只会得到两套规则。

export type MotionProp = {
  name: string;
  from: number;
  to: number;
};

export type CubicBezier = {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
};

export type Motion = {
  clipId?: string;
  /** 只作审计留痕。合成器 MUST NOT 读它决定行为（IR 不含动效语义）。 */
  effect: string;
  startMs: number;
  durationMs: number;
  easing: CubicBezier;
  props: MotionProp[];
};

export type Box = {
  x: number;
  y: number;
  w: number;
  h: number;
};

export type Layer = {
  elementId?: string;
  kind: string;
  /** bitmap-region | page-layer */
  assetMode: string;
  box: Box;
  clips?: Motion[];
  /**
   * 本层「只含自己」的位图，尺寸等于 box。缺省时退回「从 baseImage 按 box 偏移裁切」——
   * 那条路径在嵌套情形下会把内部元素一起带出来（ADR-9）。
   */
  image?: string;
};

export type Transition = {
  /** none | fade | slide | zoom */
  type: string;
  durationMs?: number;
};

/**
 * 页级焦点：从 startMs 起，「此刻讲的是谁」切换到 elementId，直到下一段开始。
 *
 * 它不含动效语义，只是一条时间—对象的对照表。非焦点的压暗量由 `focusDim`
 * 推导（与渲染器共用同一份代码），因此两端不会算出两种观感。
 */
export type FocusSpan = {
  startMs: number;
  elementId: string;
  /** 切换过渡时长，缺省 240ms。 */
  fadeMs?: number;
};
