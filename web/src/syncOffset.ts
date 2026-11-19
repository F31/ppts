// 字幕显示偏移（毫秒）的单一来源。
//
// 两类场景：
//   1) 编辑器预览：用户在播放器里实时微调，草稿存 localStorage，按 timelineKey/projectId 索引；
//      导出时读出草稿固化进成品与分享包（见 createArtifactExport 的 displayOffsetMs）。
//   2) 成品预览 / 分享播放器：manifest 带成品固化值，只读它，不再看 localStorage。
// 这样一份成品在三个入口表现一致，而编辑器仍保留"边看边调"的校准能力。

export const SYNC_OFFSET_MIN = -2000;
export const SYNC_OFFSET_MAX = 2000;

export function syncOffsetKey(timelineKey: string | undefined, projectId: string | undefined): string {
  return `ppts-sync-${timelineKey || projectId}`;
}

export function clampSyncOffset(ms: number): number {
  if (!Number.isFinite(ms)) return 0;
  return Math.max(SYNC_OFFSET_MIN, Math.min(SYNC_OFFSET_MAX, ms));
}

/** 读编辑器草稿（localStorage）。隐私模式/未设置时返回 0。 */
export function readDraftOffset(timelineKey: string | undefined, projectId: string | undefined): number {
  try {
    const raw = window.localStorage.getItem(syncOffsetKey(timelineKey, projectId));
    return raw ? clampSyncOffset(Number(raw)) : 0;
  } catch {
    return 0;
  }
}

export function writeDraftOffset(timelineKey: string | undefined, projectId: string | undefined, ms: number): void {
  try {
    window.localStorage.setItem(syncOffsetKey(timelineKey, projectId), String(clampSyncOffset(ms)));
  } catch {
    /* 隐私模式等无法写入时忽略 */
  }
}

export function clearDraftOffset(timelineKey: string | undefined, projectId: string | undefined): void {
  try {
    window.localStorage.removeItem(syncOffsetKey(timelineKey, projectId));
  } catch {
    /* ignore */
  }
}

/**
 * 播放器应采用的偏移：成品 manifest 带固化值（含 0）时以它为准；否则（编辑器实时预览）
 * 用 localStorage 草稿。判断依据是字段是否存在，而非是否为 0——0 是合法的"已校准为无偏移"。
 */
export function effectiveOffsetMs(
  manifestOffset: number | undefined,
  timelineKey: string | undefined,
  projectId: string | undefined,
): number {
  if (manifestOffset !== undefined) return clampSyncOffset(manifestOffset);
  return readDraftOffset(timelineKey, projectId);
}
