/**
 * 轮询退避纯函数：失败后下一次轮询间隔。
 * 1x → 2x → 4x …（按失败次数指数增长），封顶 maxBackoffMs；成功（failures<=0）回归 base。
 * 抽成无 React 依赖的纯函数，便于单测反向验证（旧实现「失败后照打原节奏」会让增长断言翻红）。
 */
export function pollingBackoffMs(base: number, failures: number, maxBackoffMs: number): number {
  if (failures <= 0) return base;
  const scaled = base * 2 ** (failures - 1);
  return Math.min(scaled, maxBackoffMs);
}
