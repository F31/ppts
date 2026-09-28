import { useCallback, useEffect, useRef } from 'react';
import { pollingBackoffMs } from './polling';

export interface PollingOptions {
  /** 基础轮询间隔（毫秒） */
  intervalMs: number;
  /** 是否启用轮询，默认 true。false 时不持有任何定时器。 */
  enabled?: boolean;
  /** 挂载/依赖变化时是否立即执行一次，默认 true。 */
  immediate?: boolean;
  /** 失败指数退避上限（毫秒），默认 30_000。 */
  maxBackoffMs?: number;
}

export interface PollingController {
  /** 立即停止轮询（例如任务已达终态，无需继续）。依赖变化重新满足 enabled 时会自动重启。 */
  stop: () => void;
}

/**
 * 统一轮询 hook，收敛此前散落的 3 处 setInterval（Jobs 1 处、ProjectEditor 2 处）。
 * 修复两类真缺陷（实测推翻「仅 1 处」前提）：
 *  1. 页面隐藏（document.hidden）时仍照常打请求 → 隐藏即暂停、清空定时器，显示后补一次；
 *  2. 回调失败无退避、按原节奏硬打 → 指数退避（pollingBackoffMs），成功后重置。
 * 回调 Promise 拒绝统一吞入 try/catch，避免「void fn()」造成的未处理 promise 异常。
 */
export function usePolling(
  fn: () => void | Promise<void>,
  options: PollingOptions,
): PollingController {
  const { intervalMs, enabled = true, immediate = true, maxBackoffMs = 30_000 } = options;

  const fnRef = useRef(fn);
  fnRef.current = fn;

  const stoppedRef = useRef(false);
  const timerRef = useRef<number | undefined>(undefined);

  const clear = useCallback(() => {
    if (timerRef.current !== undefined) {
      window.clearTimeout(timerRef.current);
      timerRef.current = undefined;
    }
  }, []);

  const stop = useCallback(() => {
    stoppedRef.current = true;
    clear();
  }, [clear]);

  useEffect(() => {
    if (!enabled) return;
    stoppedRef.current = false;
    let failures = 0;
    let nextDelay = intervalMs;

    const runOnce = async () => {
      try {
        await fnRef.current();
        failures = 0;
        nextDelay = intervalMs;
      } catch {
        failures += 1;
        nextDelay = pollingBackoffMs(intervalMs, failures, maxBackoffMs);
      }
    };

    const schedule = () => {
      if (stoppedRef.current || document.hidden) return;
      timerRef.current = window.setTimeout(() => {
        void runOnce().finally(schedule);
      }, nextDelay);
    };

    const onVisibility = () => {
      if (document.hidden) {
        clear();
      } else if (!stoppedRef.current) {
        // 回到前台：补一次，再按当前节奏继续。
        void runOnce().finally(schedule);
      }
    };

    if (immediate && !document.hidden) {
      void runOnce().finally(schedule);
    } else {
      schedule();
    }

    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      clear();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [enabled, intervalMs, immediate, maxBackoffMs, clear]);

  return { stop };
}

export { pollingBackoffMs };
