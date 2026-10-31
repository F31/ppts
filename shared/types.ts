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
   * 缩放锚点：center（缺省）| bottom | top | left | right。
   * 柱子按 scaleY 居中缩放会同时向上、向下长穿基线——锚底才是「从基线长上去」。
   */
  origin?: string;
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
 * 页级焦点：从 startMs 起，「此刻讲的是谁」切换到 elementId。
 *
 * 它不含动效语义，只是一条时间—对象的对照表。非焦点的压暗量由 `focusDim`
 * 推导（与渲染器共用同一份代码），因此两端不会算出两种观感。
 */
export type FocusSpan = {
  startMs: number;
  elementId: string;
  /** 放手的时刻：到此整页回到全亮，直到下一段焦点开始。缺省表示保持到下一段。 */
  endMs?: number;
  /** 切换过渡时长，缺省 240ms。 */
  fadeMs?: number;
};

/**
 * 页内镜头：一页里按时间换一次底图（全图 → 特写）。
 *
 * 它只换**图**，不换时间轴：`layers` 的 clips 仍是页内相对毫秒，跨镜头不重建层
 * （重建会让元素重新入场）。`crop` 非空的镜头里层不显示——层的 box 是整页坐标，
 * 局部放大后同一套坐标必然错位，而错位不会报错，只会看起来"元素飘了"。
 *
 * 字段与 `internal/render/ir.go` 的 Shot、`internal/share/manifest.go` 的
 * SlideShot 逐一对齐。
 */
export type Shot = {
  index: number;
  /** 铺满舞台的图：整页图，或裁切放大后的特写图。 */
  image: string;
  /** 该图取自原页的哪一块（原页像素坐标）；缺省表示整页。 */
  crop?: Box;
  /** 页内相对起点，必须等于上一镜的终点。 */
  startMs: number;
  durationMs: number;
  /** 本镜开头的叠化时长；0 = 硬切。叠化占**本镜**的开头，与页间转场同语义。 */
  crossFadeMs: number;
  /** 镜头内的相机运动关键帧（归一化坐标）；缺省表示静态特写。仅用于带 crop 的特写。 */
  camera?: CameraKeyframe[];
};

/** 镜头图局部的归一化相机关键帧：x/y 为视口中心（归一化到镜头图，0.5=中心）；合法值 MUST
 *  落在覆盖安全区间 [0.5/scale, 1-0.5/scale]（由校验+运行时 clamp 保证不露白）。scale ≥ 1
 *  相对「图覆盖舞台」。与 Go 侧 render.CameraKeyframe / CameraTransform 同语义。 */
export type CameraKeyframe = {
  atMs: number;
  x: number;
  y: number;
  scale: number;
};
