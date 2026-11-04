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

import type { FocusTarget, ScriptSegment, SegmentFocus } from './types';

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
