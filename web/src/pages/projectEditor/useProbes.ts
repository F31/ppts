import { useCallback, useState } from 'react';
import { describeApiError, isNotFound, isUnimplemented } from '../../apiError';
import type { ProbeId, Translate } from './model';

/**
 * 探测类请求的失败归档。
 *
 * 失败后只是"保持空态"的界面与"确实没有"完全同貌（A26 禁止的形态），
 * 因此所有探测请求统一走 reportProbe 落一份可解释的文案，并按 ProbeId 分位置渲染。
 */
export function useProbes(t: Translate) {
  const [probeErrors, setProbeErrors] = useState<Partial<Record<ProbeId, string>>>({});
  // 顶层探测的重试计数；变化即重跑全部顶层探测 effect。
  const [probesReloadKey, setProbesReloadKey] = useState(0);

  const clearProbe = useCallback((id: ProbeId) => {
    setProbeErrors((cur) => {
      if (!(id in cur)) return cur;
      const next = { ...cur };
      delete next[id];
      return next;
    });
  }, []);

  // reportProbe 是探测失败的统一入口。两类"非故障"必须放行、不得报错：
  //   ① isNotFound —— 资源确实不存在（如"尚未生成配音"）是正常业务语义；
  //   ② isUnimplemented —— 本部署未提供该能力（503 feature_disabled / 501），
  //      按 A26 应隐藏入口，而不是把部署差异渲染成故障。
  const reportProbe = useCallback(
    (id: ProbeId, err: unknown, fallbackKey: string) => {
      if (isNotFound(err) || isUnimplemented(err)) {
        clearProbe(id);
        return;
      }
      setProbeErrors((cur) => ({ ...cur, [id]: describeApiError(err, t(fallbackKey), t) }));
    },
    [clearProbe, t]
  );

  const reloadProbes = useCallback(() => setProbesReloadKey((key) => key + 1), []);

  return { probeErrors, probesReloadKey, clearProbe, reportProbe, reloadProbes };
}
