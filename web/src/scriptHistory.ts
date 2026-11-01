/**
 * scriptHistory —— 讲稿编辑的撤销/重做（栈里存整份快照，不存"操作"）。
 *
 * 与 animationTrack 的历史同一思路：这个数据量级（一页几十段文本）存快照最省心，
 * 也不会出现"重放操作序列两次得到不同结果"。
 *
 * 与动画不同的是**合并窗口**：讲稿是逐字符输入的，每个按键记一步会让撤销退化成
 * "一次删一个字"，按二十次才退回原句。所以同一个段落、间隔小于 COALESCE_MS 的连续
 * 编辑合成一步；换段或停手超过窗口才记新的一步。
 */
export type ScriptSnapshot = {
  texts: Record<string, string>;
  spokenTexts: Record<string, string>;
};

export type ScriptHistory = {
  past: ScriptSnapshot[];
  future: ScriptSnapshot[];
  /** 上一次记录的时间与段落，用于合并连续输入。 */
  lastAt: number;
  lastKey: string;
};

export const SCRIPT_HISTORY_LIMIT = 50;
/** 同一段落停手多久算"一步"。短于它视为同一次输入的延续。 */
export const SCRIPT_COALESCE_MS = 700;

export function emptyHistory(): ScriptHistory {
  return { past: [], future: [], lastAt: 0, lastKey: '' };
}

/** 快照必须深拷一份：直接存 state 引用时，后续编辑会改掉栈里的"历史"。 */
export function snapshotOf(texts: Record<string, string>, spokenTexts: Record<string, string>): ScriptSnapshot {
  return { texts: { ...texts }, spokenTexts: { ...spokenTexts } };
}

/**
 * recordEdit 在**一次编辑生效前**记下编辑前的快照。
 *
 * key 用来判断"是不是同一次连续输入"——传段落 id（或朗读文本编辑的固定标识）。
 */
export function recordEdit(
  history: ScriptHistory,
  previous: ScriptSnapshot,
  key: string,
  now: number = Date.now()
): ScriptHistory {
  const continuing =
    history.past.length > 0 &&
    history.lastKey === key &&
    now - history.lastAt <= SCRIPT_COALESCE_MS;
  if (continuing) {
    // 不记录新快照，只把窗口往后推：连续打字始终只有一步。
    return { ...history, lastAt: now };
  }
  const past = [...history.past, snapshotOf(previous.texts, previous.spokenTexts)];
  return {
    past: past.length > SCRIPT_HISTORY_LIMIT ? past.slice(past.length - SCRIPT_HISTORY_LIMIT) : past,
    future: [],
    lastAt: now,
    lastKey: key
  };
}

export function canUndo(history: ScriptHistory): boolean {
  return history.past.length > 0;
}

export function canRedo(history: ScriptHistory): boolean {
  return history.future.length > 0;
}

/**
 * undo 返回恢复后的快照与新的历史；历史为空时返回 null（调用方据此不改动任何东西）。
 *
 * 撤销后清掉合并窗口：否则接着打字会被当成"同一次输入"，与撤销前的快照合并，
 * 再撤一次就跳过了刚恢复的那一版。
 */
export function undo(
  history: ScriptHistory,
  current: ScriptSnapshot
): { history: ScriptHistory; snapshot: ScriptSnapshot } | null {
  if (history.past.length === 0) return null;
  const previous = history.past[history.past.length - 1];
  return {
    snapshot: previous,
    history: {
      past: history.past.slice(0, -1),
      future: [snapshotOf(current.texts, current.spokenTexts), ...history.future].slice(0, SCRIPT_HISTORY_LIMIT),
      lastAt: 0,
      lastKey: ''
    }
  };
}

export function redo(
  history: ScriptHistory,
  current: ScriptSnapshot
): { history: ScriptHistory; snapshot: ScriptSnapshot } | null {
  if (history.future.length === 0) return null;
  const next = history.future[0];
  return {
    snapshot: next,
    history: {
      past: [...history.past, snapshotOf(current.texts, current.spokenTexts)].slice(-SCRIPT_HISTORY_LIMIT),
      future: history.future.slice(1),
      lastAt: 0,
      lastKey: ''
    }
  };
}
