// 逐页动画编辑的纯状态层（tasks 3.1）。
//
// 与 scriptState 同一取向：把"能不能改 / 改了什么 / 冲突了怎么办"从组件里抽出来，
// 用 node 直接跑的用例钉住。组件只负责渲染与分发，不再自己判断规则。
//
// 服务端是校验的权威（V1–V12）。这里**不复制**那套规则，只做三件客户端必须自己做的事：
//   1. 取值夹紧（滑块拖到边界外不该发出一个必然 400 的请求）；
//   2. 计算"草案与已保存版本是否一致"（决定保存按钮是否可点、切页时是否要拦一下）；
//   3. 把规则编号翻成文案 key（后端返回 `rule`，前端按编号取文案，见设计方案 §5.4）。

export type AnimationStatus = 'draft' | 'approved' | 'locked';

export type AnimationClip = {
  id?: string;
  type: string;
  startMs: number;
  durationMs: number;
  easing: string;
  params?: Record<string, unknown>;
  /** onEnter | withSegment:<id> | atTime:<ms> */
  trigger: string;
  /** 跨页延续（G8）：true 时允许 startMs+durationMs 超出本页时长，由编译期在页边界拆分。 */
  continues?: boolean;
};

export type AnimationTrack = {
  elementId: string;
  kind: string;
  clips: AnimationClip[];
};

export type AnimationSpec = {
  schemaVersion?: string;
  slideId: string;
  language: string;
  sourceRevisionNo: number;
  revision: number;
  status: AnimationStatus;
  presetId: string;
  intensity: number;
  tracks: AnimationTrack[];
  /** true 表示保存时页时长未知（还没配音），渲染前必须用真实时长重校验。 */
  needsDurationRecheck?: boolean;
};

export type AnimationAnchor = {
  id: string;
  kind: string;
  text?: string;
  index: number;
  bulletIndex?: number;
};

export type AnimationSegment = { segmentId: string; index: number };

export type AnimationPreset = {
  id: string;
  label: string;
  description: string;
  intensity: number;
};

export type AnimationAnchors = {
  slideId: string;
  language: string;
  durationMs: number;
  elements: AnimationAnchor[];
  segments: AnimationSegment[];
  presets: AnimationPreset[];
};

// 服务端常量（internal/animation/model.go）。写成字面量而不是从接口读：
// 这几个值变了，客户端的夹紧范围与触发串格式都要一起改，属于契约变更。
export const MIN_INTENSITY = 0.5;
export const MAX_INTENSITY = 1.5;
export const TRIGGER_ENTER = 'onEnter';
export const TRIGGER_SEGMENT_PREFIX = 'withSegment:';
export const TRIGGER_AT_TIME_PREFIX = 'atTime:';

export type AnimationEditorState = {
  /** none = 这一页还没配置过动效。 */
  state: 'none' | AnimationStatus;
  /** 已锁定：服务端会拒绝写入，界面上不该给可点的保存。 */
  readOnly: boolean;
  /** 有规格才能预览：预览渲的是已保存的规格，不是草案。 */
  canPreview: boolean;
  /** 预览的输入是时间轴时长；没有配音时预览与保存都只能"存草稿"。 */
  needsVoice: boolean;
  editDisabled: boolean;
  editDisabledReasonKey: string;
};

export function editorStateOf(spec: AnimationSpec | null, canEdit: boolean): AnimationEditorState {
  if (!spec) {
    return {
      state: 'none',
      readOnly: false,
      canPreview: false,
      needsVoice: false,
      editDisabled: !canEdit,
      editDisabledReasonKey: canEdit ? '' : 'perm.needEditorExport'
    };
  }
  const locked = spec.status === 'locked';
  return {
    state: spec.status,
    readOnly: locked,
    canPreview: true,
    needsVoice: spec.needsDurationRecheck === true,
    editDisabled: !canEdit || locked,
    editDisabledReasonKey: locked ? 'editor.animationLocked' : canEdit ? '' : 'perm.needEditorExport'
  };
}

// draftOf 复制一份可编辑的规格。深拷贝 clips/tracks：直接改引用会让"取消编辑"
// 变成"改了又改回来"——只要有一处没还原，用户看到的就是"取消没生效"。
export function draftOf(spec: AnimationSpec): AnimationSpec {
  return {
    ...spec,
    tracks: spec.tracks.map((track) => ({
      ...track,
      clips: track.clips.map((clip) => ({ ...clip, params: clip.params ? { ...clip.params } : undefined }))
    }))
  };
}

export function clampIntensity(value: number): number {
  if (!Number.isFinite(value)) return 1;
  const clamped = Math.min(MAX_INTENSITY, Math.max(MIN_INTENSITY, value));
  // 保留两位：滑块 step=0.05 却发出 1.1500000000000001 会让"同一档强度"出现两种规格。
  return Math.round(clamped * 100) / 100;
}

export function withIntensity(draft: AnimationSpec, value: number): AnimationSpec {
  return { ...draft, intensity: clampIntensity(value) };
}

export function segmentTrigger(segmentId: string): string {
  return TRIGGER_SEGMENT_PREFIX + segmentId;
}

// segmentOfTrigger 返回 withSegment 触发器指向的分段 id；其它形式返回 ''。
export function segmentOfTrigger(trigger: string): string {
  if (!trigger.startsWith(TRIGGER_SEGMENT_PREFIX)) return '';
  return trigger.slice(TRIGGER_SEGMENT_PREFIX.length);
}

// withTrackTrigger 把某条轨道的**入场**片段改挂到指定触发器。
//
// 只改 entrance（默认就是全部 clips 里的 onEnter/withSegment 那一条）：
// 强调与退场挂的是"第几段之后持续多久"，把它们的起点也重锚等于把整条时间轴改写，
// 那不是用户在"改第 3 条要点的出现时机"时想要的结果。
export function withTrackTrigger(draft: AnimationSpec, elementId: string, trigger: string): AnimationSpec {
  return {
    ...draft,
    tracks: draft.tracks.map((track) => {
      if (track.elementId !== elementId) return track;
      return {
        ...track,
        clips: track.clips.map((clip) => (isEntranceClip(clip) ? { ...clip, trigger } : clip))
      };
    })
  };
}

// isEntranceClip：入场片段的判据是"起点由触发决定"。
// onEnter 与 withSegment 都是入场；atTime 是显式定位，不参与重锚。
function isEntranceClip(clip: AnimationClip): boolean {
  return clip.trigger === TRIGGER_ENTER || clip.trigger.startsWith(TRIGGER_SEGMENT_PREFIX);
}

// 只比较影响产物的字段。needsDurationRecheck 是服务端算出来的提示，
// 把它算进"脏"判据会让刚保存完的草案立刻又显示未保存。
export function specFingerprint(spec: AnimationSpec): string {
  return JSON.stringify({
    presetId: spec.presetId,
    intensity: clampIntensity(spec.intensity),
    tracks: spec.tracks.map((track) => ({
      elementId: track.elementId,
      kind: track.kind,
      clips: track.clips.map((clip) => ({
        id: clip.id ?? '',
        type: clip.type,
        startMs: clip.startMs,
        durationMs: clip.durationMs,
        easing: clip.easing,
        trigger: clip.trigger,
        continues: clip.continues ?? false,
        params: clip.params ?? {}
      }))
    }))
  });
}

export function isDirty(draft: AnimationSpec | null, saved: AnimationSpec | null): boolean {
  if (!draft || !saved) return false;
  return specFingerprint(draft) !== specFingerprint(saved);
}

export type AnimationSavePayload = {
  expectedRevision: number;
  presetId: string;
  intensity: number;
  tracks: AnimationTrack[];
};

export function savePayloadOf(draft: AnimationSpec): AnimationSavePayload {
  return {
    expectedRevision: draft.revision,
    presetId: draft.presetId,
    intensity: clampIntensity(draft.intensity),
    tracks: draft.tracks.map((track) => ({
      elementId: track.elementId,
      kind: track.kind,
      clips: track.clips.map((clip) => ({
        id: clip.id,
        type: clip.type,
        startMs: clip.startMs,
        durationMs: clip.durationMs,
        easing: clip.easing,
        trigger: clip.trigger,
        continues: clip.continues ?? false,
        params: clip.params
      }))
    }))
  };
}

// animationRuleKey 把服务端的规则编号翻成文案 key。
//
// 编号取值见 internal/animation/validate.go（V0 结构、V1 类型、V2 锚点、V3 时间、
// V4 重叠、V5 时长、V6 页预算、V7 分段、V8 预设、V11 数量、V12 字节）。
// 不复制校验逻辑，只翻译编号——服务端改了规则文案，前端跟着换 i18n 即可。
//
// 未登记的编号返回空串，由调用方回落显示服务端 message：宁可显示一句英文，
// 也不要把规则错配到另一条文案上——那会让用户照着错的提示改数据。
const RULE_KEYS: Record<string, string> = {
  V0: 'editor.animationRuleV0',
  V1: 'editor.animationRuleV1',
  V2: 'editor.animationRuleV2',
  V3: 'editor.animationRuleV3',
  V4: 'editor.animationRuleV4',
  V5: 'editor.animationRuleV5',
  V6: 'editor.animationRuleV6',
  V7: 'editor.animationRuleV7',
  V8: 'editor.animationRuleV8',
  V11: 'editor.animationRuleV11',
  V12: 'editor.animationRuleV12'
};

export function animationRuleKey(rule: string | undefined): string {
  if (!rule) return '';
  return RULE_KEYS[rule.toUpperCase()] ?? '';
}

// animationSummary 给面板顶部一行"配了几条 / 覆盖几页"的信息。
//
// 只统计**元素级**轨道：整页轨道没有对应的锚点，算进分母会让"3/5 条要点"变成
// 一个用户无法从页面上数出来的数。
export type AnimationSummary = {
  tracks: number;
  bullets: number;
  animatedBullets: number;
  segmentAligned: boolean;
};

export function summarize(draft: AnimationSpec | null, anchors: AnimationAnchors | null): AnimationSummary {
  if (!draft || !anchors) {
    return { tracks: 0, bullets: 0, animatedBullets: 0, segmentAligned: false };
  }
  const bulletIds = new Set(anchors.elements.filter((el) => el.kind === 'bullet').map((el) => el.id));
  let animatedBullets = 0;
  let segmentAligned = false;
  let tracks = 0;
  for (const track of draft.tracks) {
    if (track.kind === 'page') continue;
    if (track.clips.length === 0) continue;
    tracks += 1;
    if (bulletIds.has(track.elementId)) animatedBullets += 1;
    if (track.clips.some((clip) => clip.trigger.startsWith(TRIGGER_SEGMENT_PREFIX))) {
      segmentAligned = true;
    }
  }
  return { tracks, bullets: bulletIds.size, animatedBullets, segmentAligned };
}

// anchorLabel 给轨道一个用户认得出的名字：优先显示文字，其次"第 N 条要点"，
// 最后才退回元素编号（色块/图标没有文字，但至少能靠序号定位）。
export function anchorLabel(anchors: AnimationAnchors | null, elementId: string, fallback: string): string {
  const anchor = anchors?.elements.find((el) => el.id === elementId);
  if (!anchor) return fallback;
  if (anchor.text) return anchor.text;
  if (anchor.bulletIndex) return `#${anchor.bulletIndex}`;
  return `#${anchor.index}`;
}
