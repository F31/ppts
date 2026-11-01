// 讲稿绑定预填的复核层（tasks 4.1–4.3）。
//
// 预填一次会改写整页几十条 clip：把「按顺序淡入」换成「讲到哪条出现哪条」。用户不可能
// 逐条读，所以这一层只做一件事——把机器猜的绑定按来源摊开、按最该怀疑的顺序排，
// 让复核成本从 O(全部) 降到 O(最可疑的那批)。
//
// 两个必须写死的语义：
//
// 1. 「撤销」撤销的是**绑定**，不是动效。撤销 = 触发器改回 onEnter + 清掉来源标注，
//    clip 本体留着。删掉 clip 会让元素失去入场（而界面里没有"加一条 clip"的入口），
//    撤销就变成一次不可逆的破坏；而清掉标注之后，这条 clip 不再算预填产物，
//    服务端的 ReconcileTriggerSources 会把它记成 manual（人已经接手了）。
//
// 2. 排序不是纯按置信度，原因见 reviewOrder 的注释。

// 显式带 .ts 后缀：本模块被 node --experimental-strip-types animation-check.mjs 直接加载。
import { TRIGGER_ENTER, TRIGGER_SEGMENT_PREFIX } from './animationState.ts';
import type { AnimationClip, AnimationSpec } from './animationState.ts';

/** 复核顺序：无字面依据、或一次会带出一片的规则排前面。理由见 reviewOrder。 */
const REVIEW_ORDER: Record<string, number> = {
  sibling: 0,
  // 模型给的绑定排在很前面：它是唯一一条「证据来自模型自述」的来源，
  // 而实测在 script 档上也只有 0.833——比规则的结构化页（0.972）低，
  // 且模型看不见容器（四步流程页实测漏 9/18），漏绑比错绑更不容易被发现。
  model: 1,
  prior: 2,
  structural: 2,
  count: 4,
  ordinal: 5,
  spatial: 6,
  text: 7,
  manual: 8
};

/** 来源的文案 key。未登记的来源返回空串，调用方回落到原始 id。 */
const SOURCE_KEYS: Record<string, string> = {
  text: 'editor.animationSourceText',
  count: 'editor.animationSourceCount',
  ordinal: 'editor.animationSourceOrdinal',
  spatial: 'editor.animationSourceSpatial',
  sibling: 'editor.animationSourceSibling',
  prior: 'editor.animationSourcePrior',
  structural: 'editor.animationSourceStructural',
  model: 'editor.animationSourceModel',
  manual: 'editor.animationSourceManual'
};

/** 结构信号的文案 key（与 internal/animation/bind/bind.go 的常量同名）。 */
const SIGNAL_KEYS: Record<string, string> = {
  markers: 'editor.animationSignalMarkers',
  clusters: 'editor.animationSignalClusters',
  directions: 'editor.animationSignalDirections',
  'script-structure': 'editor.animationSignalScriptStructure',
  'script-flat': 'editor.animationSignalScriptFlat',
  'script-absent': 'editor.animationSignalScriptAbsent'
};

export type PrefillBinding = {
  elementId: string;
  source: string;
  /** 0 表示「这条规则没有字面依据」，不是「相似度为 0」（见 hasConfidence）。 */
  confidence: number;
  hasConfidence: boolean;
  trigger: string;
  /** withSegment 指向的分段 id；手动改成页首后为空。 */
  segmentId: string;
};

/**
 * prefillBindings 列出「带来源标注的绑定」。
 *
 * 一条轨道只取**第一条有来源的 clip**：预填每条轨道只产出一条 clip，而人工编辑可能
 * 追加强调/退场，那些没有来源，重复列出来只会让同一条绑定在表里出现两次。
 */
export function prefillBindings(spec: AnimationSpec | null): PrefillBinding[] {
  if (!spec) return [];
  const out: PrefillBinding[] = [];
  for (const track of spec.tracks) {
    if (track.kind === 'page') continue;
    const clip = track.clips.find((item) => (item.triggerSource ?? '') !== '');
    if (!clip) continue;
    const source = clip.triggerSource ?? '';
    // confidence 缺省/0 = 没有字面依据。两个含义分开记，UI 才不会把「无证据」写成「0 分」。
    const confidence = typeof clip.confidence === 'number' && clip.confidence > 0 ? clip.confidence : 0;
    out.push({
      elementId: track.elementId,
      source,
      confidence,
      hasConfidence: confidence > 0,
      trigger: clip.trigger,
      segmentId: clip.trigger.startsWith(TRIGGER_SEGMENT_PREFIX)
        ? clip.trigger.slice(TRIGGER_SEGMENT_PREFIX.length)
        : ''
    });
  }
  return out;
}

/** sourceGroups 按来源聚合条数，顺序固定（未知来源排在已知之后，按 id 升序）。 */
export function sourceGroups(bindings: PrefillBinding[]): { source: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const item of bindings) counts.set(item.source, (counts.get(item.source) ?? 0) + 1);
  return [...counts.entries()]
    .map(([source, count]) => ({ source, count }))
    .sort((a, b) => rankOf(a.source) - rankOf(b.source) || a.source.localeCompare(b.source));
}

/**
 * reviewOrder 按「最该先看」排序。
 *
 * 规格原文是「置信度升序」，但**实测置信度只有字面规则非零**：bind.Binding.Score 的
 * 定义就是字面相似度，其余六条规则恒为 0（代码里刻意如此——硬塞一个数字进去会让人
 * 误以为可以拿来排序）。按纯置信度升序会把六条规则挤在 0 上，排序等于没排。
 *
 * 所以改成两级，理由都能落到具体行为上：
 *   1. `sibling`（同块带出）最先：它是唯一会「一次带出一整片」的规则，锚点错一处就错一片；
 *      defaults.go 里那句「先看同块带出的那 9 条」说的就是这件事。
 *   2. 其余无字面依据的规则按固定顺序（先验 → 结构 → 计数 → 序数 → 方位），
 *      有字面依据的 `text` 按置信度升序（相似度低的先看），人工确认的 `manual` 最后。
 *
 * 同一层里按元素 id 兜底，保证同一份规格每次渲染顺序一致（复核表跳动会让人点错行）。
 */
export function reviewOrder(bindings: PrefillBinding[]): PrefillBinding[] {
  return [...bindings].sort((a, b) => {
    const rank = rankOf(a.source) - rankOf(b.source);
    if (rank !== 0) return rank;
    if (a.source === 'text' && b.source === 'text' && a.confidence !== b.confidence) {
      return a.confidence - b.confidence;
    }
    return a.elementId.localeCompare(b.elementId);
  });
}

function rankOf(source: string): number {
  return REVIEW_ORDER[source] ?? Object.keys(REVIEW_ORDER).length;
}

/** filterBySource：空串 = 全部。 */
export function filterBySource(bindings: PrefillBinding[], source: string): PrefillBinding[] {
  if (!source) return bindings;
  return bindings.filter((item) => item.source === source);
}

/**
 * 「为什么没用上模型」的文案 key。
 *
 * 这几个原因对用户意味着完全不同的下一步：timeout/error 值得重试，quota 要去充值，
 * no_model 要找管理员配模型，stale 只需要重新点一次。合成一句「模型不可用」
 * 会让人反复点同一个不会成功的按钮。
 */
const MODEL_FALLBACK_KEYS: Record<string, string> = {
  timeout: 'editor.animationPrefillReasonTimeout',
  quota: 'editor.animationPrefillReasonQuota',
  error: 'editor.animationPrefillReasonError',
  invalid_json: 'editor.animationPrefillReasonInvalidJson',
  no_valid_bindings: 'editor.animationPrefillReasonNoBindings',
  no_model: 'editor.animationPrefillReasonNoModel',
  stale: 'editor.animationPrefillReasonStale'
};

export function prefillModelFallbackKey(reason?: string): string {
  if (!reason) return '';
  return MODEL_FALLBACK_KEYS[reason] ?? '';
}

export function prefillSourceKey(source: string): string {
  return SOURCE_KEYS[source] ?? '';
}

export function prefillSignalKey(signal: string): string {
  return SIGNAL_KEYS[signal] ?? '';
}

/**
 * 低置信提示的文案 key，按「缺的是哪一侧」分叉。
 *
 * 四种情况的补救动作不同：layout 该动版面，script 该改写讲解词，no-script 该先写稿
 * （用户手里还没有可改的东西），both 两边都缺。留一句通用文案，用户会照着去改错的地方。
 *
 * no-script 这一条在预填面板里不单独渲染——同一页的「预填已跳过」已经把
 * 「没有讲稿分段」说过一遍了，再说一遍只是噪音。这里保留映射，是为了别的调用方
 * 拿到 no-script 时不至于退回到「没有检测到结构化版面信号」——那句话对一页
 * 有编号有卡片的版面是假话。
 *
 * 未登记的 reason 退回 layout 那句：band=low-confidence 且没给原因时，
 * 版面确实没被判成结构化。
 */
export function prefillLowConfidenceKey(reason?: string): string {
  if (reason === 'no-script') return 'editor.animationPrefillLowConfidenceNoScript';
  if (reason === 'script') return 'editor.animationPrefillLowConfidenceScript';
  if (reason === 'both') return 'editor.animationPrefillLowConfidenceBoth';
  return 'editor.animationPrefillLowConfidenceLayout';
}

/**
 * revokeSource 撤销某个来源的全部绑定（tasks 4.1/4.3）。
 *
 * 返回新的规格与被改动条数。改动 = 触发器改回 onEnter、起点归零、来源与置信度清空。
 * 起点必须一并归零：入场 clip 的起点由 trigger 推导，留着 withSegment 时代的
 * stagger 值会让时间轴上的色块停在 280ms，而实际从页首开始——界面上在说谎。
 */
export function revokeSource(spec: AnimationSpec, source: string): { spec: AnimationSpec; count: number } {
  return revoke(spec, (clip) => (clip.triggerSource ?? '') === source);
}

/** revokeBinding 撤销单个元素的绑定（复核表里的行内操作）。 */
export function revokeBinding(spec: AnimationSpec, elementId: string): { spec: AnimationSpec; count: number } {
  return revoke(spec, (clip, trackID) => trackID === elementId && (clip.triggerSource ?? '') !== '');
}

function revoke(
  spec: AnimationSpec,
  match: (clip: { triggerSource?: string }, trackID: string) => boolean
): { spec: AnimationSpec; count: number } {
  let count = 0;
  const tracks = spec.tracks.map((track) => {
    if (track.kind === 'page') return track;
    let changed = false;
    const clips = track.clips.map((clip) => {
      if (!match(clip, track.elementId)) return clip;
      changed = true;
      count += 1;
      const next = { ...clip, trigger: TRIGGER_ENTER, startMs: 0 };
      // 用 delete 而不是解构：解构出的未使用变量会被 noUnusedLocals 拦下，
      // 而且显式删两个字段读起来就是「清掉标注」这件事本身。
      delete next.triggerSource;
      delete next.confidence;
      return next;
    });
    return changed ? { ...track, clips } : track;
  });
  if (count === 0) return { spec, count: 0 };
  return { spec: { ...spec, tracks }, count };
}

/**
 * carryBindings 把上一份草案的「入场时机 + 来源」带到新生成的规格上。
 *
 * 为什么需要：换风格（预设）会整份重新生成。不搬的话，用户刚复核过的几十条绑定、
 * 刚撤销过的判定，全部退回「按顺序淡入」——而用户点的只是「换个风格」，
 * 想换的是怎么出现，不是什么时候出现。
 *
 * 只搬入场 clip（onEnter / withSegment）的 trigger 与标注，**不搬 atTime**：
 * 那是时间轴上的显式定位，不属于绑定结论；也不搬 startMs（入场起点由 trigger 推导，
 * 段内错落该按新预设的节奏走）。
 */
export function carryBindings(previous: AnimationSpec | null, next: AnimationSpec): AnimationSpec {
  if (!previous) return next;
  const entranceOf = new Map<string, AnimationClip>();
  for (const track of previous.tracks) {
    if (track.kind === 'page') continue;
    const clip = track.clips.find(isEntrance);
    if (clip) entranceOf.set(track.elementId, clip);
  }
  if (entranceOf.size === 0) return next;

  return {
    ...next,
    tracks: next.tracks.map((track) => {
      if (track.kind === 'page') return track;
      const old = entranceOf.get(track.elementId);
      if (!old) return track;
      let carried = false;
      const clips = track.clips.map((clip) => {
        // 只搬第一条入场 clip：一套预设里强调/退场排在入场之后，它们不该继承绑定。
        if (carried || !isEntrance(clip)) return clip;
        carried = true;
        const merged: AnimationClip = { ...clip, trigger: old.trigger };
        if (old.triggerSource) merged.triggerSource = old.triggerSource;
        if (typeof old.confidence === 'number' && old.confidence > 0) merged.confidence = old.confidence;
        return merged;
      });
      return { ...track, clips };
    })
  };
}

function isEntrance(clip: AnimationClip): boolean {
  return clip.trigger === TRIGGER_ENTER || clip.trigger.startsWith(TRIGGER_SEGMENT_PREFIX);
}

/**
 * defaultSpecQuery 是「生成默认规格」的查询参数。
 *
 * 单独抽出来是为了能被 node 用例钉住：`prefill=true` 少写一个字符，服务端就当没开，
 * 用户看到的是「点了自动绑定，结果和按顺序淡入一模一样」——而且不报错。
 */
export function defaultSpecQuery(presetId: string, prefill: boolean): Record<string, string> {
  const query: Record<string, string> = { default: 'true' };
  if (presetId) query.preset = presetId;
  if (prefill) query.prefill = 'true';
  return query;
}
