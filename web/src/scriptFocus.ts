// 讲稿段落的「焦点」投影。
//
// 单独成纯函数文件的原因：这块逻辑决定了**保存时会不会误删用户已经选好的焦点**，
// 必须能被 node 直接加载并断言（见 script-focus-check.mjs）。放在组件里就只能靠肉眼看。
//
// 三态语义（与服务端一致，见 internal/api/script.go 的 focusFromProto）：
//   - 字段缺席      = 这次保存不表态，服务端保留原值
//   - {shapeId:''}  = 显式清空
//   - 有 shapeId    = 指过去
// 把"缺席"当成"清空"，用户改一个错别字就会把整页焦点抹掉，而且存稿、接口、日志都不报错。

import type {
  FocusSuggestion,
  FocusSuggestionCandidate,
  FocusTarget,
  ScriptSegment,
  SegmentFocus
} from './types';

// EMPTY_FOCUS 是"明确不指"的取值。
export const EMPTY_FOCUS: SegmentFocus = { shapeId: '', barId: '' };

// FocusMap 按段记名：null = 不表态（保存时省略字段）。
export type FocusMap = Record<string, SegmentFocus | null>;

// initialFocusMap 把服务端的段落焦点投影成本地状态。
export function initialFocusMap(segments: ScriptSegment[]): FocusMap {
  const out: FocusMap = {};
  for (const segment of segments) {
    out[segment.segmentId] = segment.focus && segment.focus.shapeId ? segment.focus : null;
  }
  return out;
}

// applyFocus 把焦点贴回段落：不表态的段落**没有** focus 字段（而不是 null）。
//
// 写成 { focus: null } 会被 JSON.stringify 保留成 "focus": null，而服务端把
// "消息在场但 shapeId 为空"当成清空 —— 语义就从"保留"变成了"删除"。
export function applyFocus(segments: ScriptSegment[], focus: FocusMap): ScriptSegment[] {
  return segments.map((segment) => {
    const chosen = focus[segment.segmentId];
    const next: ScriptSegment = { ...segment };
    if (chosen === null || chosen === undefined) delete next.focus;
    else next.focus = chosen;
    return next;
  });
}

// focusLabelOf 把已选焦点翻成人读的标签。
//
// 找不到对应候选时显示 id 而不是悄悄显示"未指定"：源版本换过、或这一页的候选清单
// 没加载成功时，用户需要看到"它指着一个现在列不出来的东西"，而不是以为焦点丢了。
export function focusLabelOf(chosen: SegmentFocus | null, targets: FocusTarget[]): string {
  if (!chosen || !chosen.shapeId) return '';
  const hit = targets.find(
    (target) => target.shapeId === chosen.shapeId && (target.barId ?? '') === (chosen.barId ?? '')
  );
  if (hit) return hit.label;
  return chosen.barId ? `${chosen.shapeId} · ${chosen.barId}` : chosen.shapeId;
}

// isSameFocus 判断两个目标是否指向同一处（用于列表里的选中态）。
export function isSameFocus(a: SegmentFocus | null | undefined, b: SegmentFocus | null | undefined): boolean {
  return (a?.shapeId ?? '') === (b?.shapeId ?? '') && (a?.barId ?? '') === (b?.barId ?? '');
}

// ---- 焦点建议（只读端点 → 本地待确认状态）----
//
// 服务端给的是**一组候选**而不是一个：真实编号卡片页上一段话会命中多个元素，
// 从里面替用户挑一个就是「静默绑错」。这里只把「唯一候选」的段批量采纳，
// 多候选的段留在界面上让人逐段挑；两者都不写库，写库仍由既有保存通道完成。

export function suggestionsForSegment(
  suggestions: FocusSuggestion[] | null | undefined,
  segmentId: string
): FocusSuggestionCandidate[] {
  if (!suggestions) return [];
  const hit = suggestions.find((item) => item.segmentId === segmentId);
  return hit?.candidates ?? [];
}

// suggestedOptions 把建议映射成界面能显示的选项。
// 建议里只有 shapeId，没有标签；且只保留候选清单里真有的目标——
// 「编辑器里选得到的，就是成片里指得到的」这条约束不能被建议绕过。
export function suggestedOptions(
  suggestions: FocusSuggestion[] | null | undefined,
  segmentId: string,
  targets: FocusTarget[]
): Array<{ focus: SegmentFocus; label: string; source: string }> {
  const out: Array<{ focus: SegmentFocus; label: string; source: string }> = [];
  for (const c of suggestionsForSegment(suggestions, segmentId)) {
    if (!c.shapeId) continue;
    const hit = targets.find(
      (target) => target.shapeId === c.shapeId && (target.barId ?? '') === (c.barId ?? '')
    );
    if (!hit) continue;
    out.push({
      focus: { shapeId: c.shapeId, barId: c.barId ?? '' },
      label: hit.label,
      source: c.source
    });
  }
  return out;
}

// acceptUniqueSuggestions 只把「唯一候选」的建议采纳进本地 FocusMap。
//
// 四条口径，少一条就会把机器猜的东西写死：
//   · 多候选不采纳（人挑）；
//   · 已经指过的段不覆盖（人工优先）；
//   · 候选必须真的在可选清单里 —— 逐段菜单有 suggestedOptions 过滤，批量路径若
//     漏掉这一条，服务端返回脏数据或版本串页时就会写入成片指不到的目标；
//   · 未采纳的段保持原值：null 仍是 null，绝不能变成 undefined 或 focus:null
//     （后者经 JSON.stringify 会变成 "focus": null，服务端按清空处理）。
// targets 是**必传**的：留成可选就等于给批量采纳开一条绕过闸门的旁路。
// 返回的是新的 FocusMap，调用方仍需经既有保存通道提交——本函数不碰网络。
export function acceptUniqueSuggestions(
  segments: ScriptSegment[],
  focus: FocusMap,
  suggestions: FocusSuggestion[] | null | undefined,
  targets: FocusTarget[]
): { focus: FocusMap; accepted: string[] } {
  if (!suggestions || suggestions.length === 0) return { focus, accepted: [] };
  const next: FocusMap = { ...focus };
  const accepted: string[] = [];
  for (const segment of segments) {
    const current = next[segment.segmentId] ?? null;
    if (current && current.shapeId) continue;
    const sug = suggestions.find((item) => item.segmentId === segment.segmentId);
    if (!sug || sug.candidates.length !== 1) continue;
    // 走与逐段菜单完全相同的过滤；只有「规则只给一个、且这一个真的可选」才采纳。
    const options = suggestedOptions(suggestions, segment.segmentId, targets);
    if (options.length !== 1) continue;
    next[segment.segmentId] = options[0].focus;
    accepted.push(segment.segmentId);
  }
  return { focus: next, accepted };
}
