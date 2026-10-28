// CompiledTimeline 的 TypeScript 镜像。
//
// 与 internal/render/ir.go 逐字段对齐：JSON 标签、可空性、数值单位（像素/毫秒）。
// 这里刻意不引入 zod 之类的运行时校验：校验的权威在 Go 侧（Validate 已在提交前跑过），
// Node 端重复校验只会得到两套规则。

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
};

export type Transition = {
  /** none | fade | slide | zoom */
  type: string;
  durationMs?: number;
};

export type Page = {
  index: number;
  slideId: string;
  durationMs: number;
  /** 相对工作目录的路径（对应 Remotion publicDir）。 */
  baseImage: string;
  /**
   * 去残影底板：把参与元素动画的区域用周围底色填平后的整页图。
   * 缺省时退回 baseImage —— 等价于旧行为（入场动画会有残影）。
   */
  plateImage?: string;
  layers?: Layer[];
  transition: Transition;
};

export type Font = {
  family: string;
  version: string;
};

export type Diagnostic = {
  code: string;
  page: number;
  elementId?: string;
  clipId?: string;
  message: string;
};

export type Timeline = {
  schemaVersion: string;
  fps: number;
  width: number;
  height: number;
  background: string;
  fonts?: Font[];
  pages: Page[];
  warnings?: Diagnostic[];
};
