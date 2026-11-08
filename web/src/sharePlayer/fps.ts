/**
 * 实测帧率降级（设计 §20.10 降级矩阵）：
 * `deviceMemory < 4` **或** 实测 fps < 24 → 切「静态页 + 音频 + 字幕」，等同有声幻灯。
 *
 * 为什么必须实测：deviceMemory 只有 Chromium 系提供（0.25/0.5/1/2/4/8 档），
 * 它回答的是"这台机器有多少内存"，而不是"这份包在它上面能不能跑满 30fps"。
 * 一台 8GB 但核显孱弱、或同时开着会议与 IDE 的机器，deviceMemory 判不出来。
 *
 * 判据取"连续两个窗口低于阈值"而不是单帧/单窗口：
 * 首屏解码、切页重建层、GC 都会造成一两个 100ms 的长帧，按单帧判会把正常的
 * 机器也降级——降级不可逆，误判的代价是这个人再也看不到动效。
 */
export const FPS_DEGRADE_THRESHOLD = 24;
/** 统计窗口：2s ≈ 60 帧，足够抹平单帧抖动，又不会让"卡了 5 秒"迟迟不降。 */
export const FPS_WINDOW_MS = 2000;
/**
 * 超过这个帧间隔视为"被打断"（切后台 rAF 直接停、锁屏、调试断点、长 GC），
 * 丢弃当前窗口重新计。把 90s 的停顿算进 fps 会得到 0.01fps——
 * 那是"没在渲染"，不是"渲染得慢"，两者的处置完全不同。
 */
export const FPS_MAX_GAP_MS = 250;
/** 连续多少个窗口低于阈值才降级。 */
export const FPS_DEGRADE_WINDOWS = 2;

/**
 * 帧率计。注入时间戳（`performance.now()`），不在内部读时钟，因此可在 node 里直接单测。
 *
 * 用法：每帧调一次 `frame(now)`；返回非 null 表示"一个窗口攒满了，这是该窗口的 fps"。
 */
export class FpsMeter {
  private startedAt = -1;
  private lastAt = -1;
  private frames = 0;
  private readonly windowMs: number;
  private readonly maxGapMs: number;

  // 不用参数属性（`constructor(private x)`）：node 的 --experimental-strip-types
  // 只剥类型不转译，参数属性属于"要生成代码"的写法，会让单测直接报语法错。
  constructor(windowMs: number = FPS_WINDOW_MS, maxGapMs: number = FPS_MAX_GAP_MS) {
    this.windowMs = windowMs;
    this.maxGapMs = maxGapMs;
  }

  /** 暂停、seek、重播后调用：丢掉半截窗口，避免把停顿前后的帧算进同一段。 */
  reset(): void {
    this.startedAt = -1;
    this.lastAt = -1;
    this.frames = 0;
  }

  frame(nowMs: number): number | null {
    if (this.startedAt < 0) {
      this.startedAt = nowMs;
      this.lastAt = nowMs;
      this.frames = 1;
      return null;
    }
    if (nowMs - this.lastAt > this.maxGapMs) {
      // 被打断：这一段不能代表渲染能力，重新开窗。
      this.startedAt = nowMs;
      this.lastAt = nowMs;
      this.frames = 1;
      return null;
    }
    this.lastAt = nowMs;
    this.frames += 1;
    const elapsed = nowMs - this.startedAt;
    if (elapsed < this.windowMs) return null;
    // 用帧间隔数（frames-1）而不是帧数：N 帧只产生 N-1 个间隔，除帧数会系统性高估。
    const fps = ((this.frames - 1) * 1000) / elapsed;
    this.startedAt = nowMs;
    this.frames = 1;
    return fps;
  }
}

/** 累计"连续低帧窗口数"：一个健康窗口立刻清零，不搞滑动平均（迟滞要明确）。 */
export function nextFpsStreak(streak: number, fps: number): number {
  return fps < FPS_DEGRADE_THRESHOLD ? streak + 1 : 0;
}

export function shouldDegradeFps(streak: number): boolean {
  return streak >= FPS_DEGRADE_WINDOWS;
}
