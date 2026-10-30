// CompiledTimeline 的 TypeScript 镜像（页面与时间轴部分）。
//
// 动画相关类型（`Box`/`Layer`/`Motion`/`Transition`/`CubicBezier`）已搬到仓库根的
// `shared/types.ts`——它们由 renderer 与 sharePlayer 共用。这里只 re-export，不再定义一遍：
// 两处各写一份就是两端漂移的起点。
//
// 与 `internal/render/ir.go` 逐字段对齐：JSON 标签、可空性、数值单位（像素/毫秒）。
// 这里刻意不引入 zod 之类的运行时校验：校验的权威在 Go 侧（Validate 已在提交前跑过），
// Node 端重复校验只会得到两套规则。

import type { FocusSpan, Layer, Transition } from '../../shared/types';

export type {
  Box,
  CubicBezier,
  FocusSpan,
  Layer,
  Motion,
  MotionProp,
  Transition
} from '../../shared/types';

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
  /** 页级焦点序列：此刻讲的是谁（见 shared/motion.ts 的 focusDim）。 */
  focus?: FocusSpan[];
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
