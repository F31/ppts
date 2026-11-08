// 批量套用动效的纯函数：页集合整理、结果一致性、逐页原因汇总。
//
// 单独拆出来是为了让门禁能变异测试这些边界——"勾选了 5 页但只写了 3 页"
// 这类不一致如果只靠肉眼审 UI，一定会在某次改动后悄悄回来。

export type BatchSlideResult = {
  slideId: string;
  status: 'applied' | 'skipped';
  reason?: string;
  revision?: number;
};

export type BatchSnapshot = {
  slideId: string;
  existed: boolean;
  revision?: number;
  presetId?: string;
  intensity?: number;
  tracks?: unknown[];
};

export type BatchApplyResponse = {
  sourceSlideId?: string;
  total?: number;
  applied?: number;
  results?: BatchSlideResult[];
  previous?: BatchSnapshot[];
  code?: string;
  message?: string;
};

/** 从勾选集合里去掉源页、去重，保持用户看到的页序。 */
export function batchTargets(selected: string[], sourceSlideId: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of selected) {
    const id = String(raw ?? '').trim();
    if (!id || id === sourceSlideId || seen.has(id)) continue;
    seen.add(id);
    out.push(id);
  }
  return out;
}

/**
 * 成功响应必须自洽：applied 等于逐页 applied 数，快照数等于 applied 数。
 * 后端若只回一个"成功"汇总而不给快照，撤销按钮就不能出现——宁可不显示，
 * 也不要给用户一个点了没反应的按钮。
 */
export function batchApplyConsistent(resp: BatchApplyResponse | null | undefined): boolean {
  if (!resp || !Array.isArray(resp.results) || !Array.isArray(resp.previous)) return false;
  const applied = resp.results.filter((item) => item?.status === 'applied').length;
  if (typeof resp.applied === 'number' && resp.applied !== applied) return false;
  if (resp.applied === 0) return false;
  return resp.previous.length === applied;
}

/** 预检被拒时，把逐页原因压成一句可行动的复核清单。 */
export function summarizeBatchReasons(
  results: BatchSlideResult[] | undefined,
  reasonText: (reason: string) => string
): string {
  const counts = new Map<string, number>();
  for (const item of results ?? []) {
    if (item?.status === 'skipped') {
      const key = item.reason ?? 'unknown';
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
  }
  return [...counts.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([reason, count]) => `${reasonText(reason)} × ${count}`)
    .join('、');
}

/** 撤销前自检：快照必须非空、页 ID 合法且互不重复。 */
export function restorableSnapshots(snapshots: BatchSnapshot[] | null | undefined): BatchSnapshot[] | null {
  if (!Array.isArray(snapshots) || snapshots.length === 0) return null;
  const seen = new Set<string>();
  for (const snap of snapshots) {
    const id = String(snap?.slideId ?? '').trim();
    if (!id || seen.has(id)) return null;
    seen.add(id);
  }
  return snapshots;
}
