import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react';
import { useI18n } from './i18n';
import type { ScriptMode, ScriptRevision, ScriptSegment } from './types';
import type { RewriteAction } from './api';
import { useDialogA11y } from './a11y';

export type ScriptEditorStatus = 'saved' | 'dirty' | 'saving' | 'error' | 'conflict';

// ScriptConflictError：commit 通道判定为「版本冲突」时抛出（父级已把冲突双方交给对照面板）。
// 编辑器据此**放弃**本地草案 —— 父级 conflict.localText 已保存本地文本，且「采用服务端版本 /
// 以最新版本重试」两条出口都在父级；编辑器若继续持有草案，会把用户明确放弃的文本自动写回去。
export class ScriptConflictError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ScriptConflictError';
  }
}

export type ScriptEditorHandle = {
  // flush：立即提交当前待保存草稿（Ctrl+S / 切换页面前调用）。
  flush: () => void;
  // isDirty：存在尚未落库的编辑（dirty/saving/error 均视为未保存）。
  isDirty: () => boolean;
};

type Props = {
  script: ScriptRevision;
  // slideTitle：展示给用户看的页标题，来自左侧缩略图/页清单；不要暴露内部 slideId。
  slideTitle?: string;
  onChange: (script: ScriptRevision) => void;
  // commit：把指定页（slideId）的讲稿提交到服务端。**必须支持提交非当前显示页** ——
  // 提交在途时用户可能已切到别的页，原页在途期间的编辑仍须能落库（R-13）。
  commit?: (slideId: string, segments: ScriptSegment[], expectedRevision: number) => Promise<ScriptRevision>;
  onCommitError?: (message: string) => void;
  onStatusChange?: (status: ScriptEditorStatus) => void;
  // canEdit：当前用户具 EDITOR 及以上角色（服务端 ScriptService.Update 的最低要求，script.go:55）。
  // 为 false 时段落只读、工具栏禁用，避免 Viewer/Reviewer 看到可写却必然 403 的假能力（A22）。
  canEdit?: boolean;
  // onEdited：用户实际改动讲稿时触发（用于标记"语音待更新"）。
  onEdited?: () => void;
  // onRegenerateScript：按所选模式重新生成本页讲稿（原文朗读/润色讲解/AI 生成讲解）。
  onRegenerateScript?: (mode: ScriptMode) => void;
  // scriptBusy：讲稿正在后台重新生成。
  scriptBusy?: boolean;
  // scriptStatusText：讲稿生成的状态短文案（生成中/完成/失败）。
  scriptStatusText?: string;
  // scriptError：scriptStatusText 为失败信息时置 true（红色显示）。
  scriptError?: boolean;
  // onRegenerateVoice：按当前讲稿重新生成语音（TTS）。
  onRegenerateVoice?: () => void;
  // voiceBusy：语音正在生成中（禁用按钮并显示进行中文案）。
  voiceBusy?: boolean;
  // voiceProgress：生成进度百分比；<0 表示未知（排队中）。
  voiceProgress?: number;
  // voiceStatusText：生成中的状态短文案（排队中/生成中）。
  voiceStatusText?: string;
  // voiceNeedsUpdate：讲稿在最近一次配音后又被编辑过。
  voiceNeedsUpdate?: boolean;
  // onRewriteText：段落工具栏"缩短/润色/衔接"触发 LLM 改写（同步返回新文本）。
  // 成功后的文本经本地草稿自动保存落库；失败抛错由 onRewriteError 呈现。
  onRewriteText?: (text: string, action: RewriteAction) => Promise<string>;
  // onRewriteError：改写失败（LLM 不可用/超限等）时上报错误文案。
  onRewriteError?: (message: string) => void;
  // onAddToDictionary：M4 ⑤ 读音调整弹窗"添加到租户词典"（pattern=原词, replacement=读音）。
  onAddToDictionary?: (word: string, reading: string) => void;
};

// 停顿标记：插入到 spokenText 的朗读提示；发音由 M4 ⑤ 读音 popover 经 〔读：x〕 标记处理。
const PAUSE_MARKER = '‖';

// 重新生成讲稿的三种模式（与生成面板保持一致）。
const SCRIPT_REGEN_MODES: Array<{ value: ScriptMode; labelKey: string; descKey: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', labelKey: 'editor.modes.original', descKey: 'editor.modes.originalDesc' },
  { value: 'SCRIPT_MODE_POLISH', labelKey: 'editor.modes.polish', descKey: 'editor.modes.polishDesc' },
  { value: 'SCRIPT_MODE_AI_GENERATED', labelKey: 'editor.modes.ai', descKey: 'editor.modes.aiDesc' }
];

// A10 自动保存时序：停止输入 SAVE_DEBOUNCE_MS 后落库；若上次提交仍在途，退避 SAVE_RETRY_MS 后重排。
const SAVE_DEBOUNCE_MS = 700;
const SAVE_RETRY_MS = 300;

export const ScriptEditor = forwardRef<ScriptEditorHandle, Props>(function ScriptEditor(
  { script, slideTitle, onChange, commit, onCommitError, onStatusChange, canEdit = true, onEdited, onRegenerateScript, scriptBusy, scriptStatusText, scriptError, onRegenerateVoice, voiceBusy, voiceProgress = -1, voiceStatusText, voiceNeedsUpdate, onRewriteText, onRewriteError, onAddToDictionary },
  ref
) {
  const { t } = useI18n();
  // 各分段展示文本（display==spoken，编辑同步）。
  const [texts, setTexts] = useState<Record<string, string>>(() => initialTexts(script));
  const [saveState, setSaveState] = useState<ScriptEditorStatus>('saved');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focusedId, setFocusedId] = useState<string | null>(script.segments[0]?.segmentId ?? null);
  // 段落改写（缩短/润色/衔接）进行中的段落 + 同步防重入标志（按钮禁用立即可见，防连点）。
  const [rewritingSegs, setRewritingSegs] = useState<Set<string>>(new Set());
  const rewritingRef = useRef(false);
  // rewriteNote：改写完成后的回执文案（成功/部分/模型未改动），避免"点了没反应"。
  const [rewriteNote, setRewriteNote] = useState<string | null>(null);
  // M4 ⑤ 读音调整 popover 状态。
  const [popoverOpen, setPopoverOpen] = useState(false);
  const popoverRef = useDialogA11y<HTMLDivElement>(() => setPopoverOpen(false));
  // 重新生成讲稿：模式下拉菜单 + 记住上次选择的模式（主按钮直接按该模式生成）。
  const [modeMenuOpen, setModeMenuOpen] = useState(false);
  const [lastRegenMode, setLastRegenMode] = useState<ScriptMode>('SCRIPT_MODE_POLISH');
  const modeMenuRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!modeMenuOpen) return;
    const onDocDown = (event: MouseEvent) => {
      if (modeMenuRef.current && !modeMenuRef.current.contains(event.target as Node)) setModeMenuOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setModeMenuOpen(false);
    };
    document.addEventListener('mousedown', onDocDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [modeMenuOpen]);
  const [popoverWord, setPopoverWord] = useState('');
  const [popoverReading, setPopoverReading] = useState('');
  const [affectedCount, setAffectedCount] = useState(0);
  const composingRef = useRef(false);
  const textareaRefs = useRef<Record<string, HTMLTextAreaElement | null>>({});
  // —— 未落库草案（drafts）按页记名 ——
  // 为什么必须按 slideId 记名（R-13 完整化）：待保存文本只放在本组件 state 时，切页会用新页文本
  // 覆盖 texts，同时 clearTimeout 掉为原页排的退避重排 —— 于是"提交在途期间对原页的编辑"在用户
  // 切页后既不会被提交、也不会回到父级，**永久静默丢失**（无提示、无恢复入口）。
  // 现改为：每次编辑把该页的段落快照存进草案表；提交与退避重排都以"页"为单位，
  // 切页只影响显示，不影响原页草案的续传。
  type Draft = { segments: ScriptSegment[]; seq: number };
  const draftsRef = useRef<Map<string, Draft>>(new Map());
  // revisionsRef：每页最后已知的服务端 revision（切页后 props 不再提供原页的 revision）。
  const revisionsRef = useRef<Map<string, number>>(new Map());
  // inFlightRef：正在提交的页集合 —— 每页至多一个在途提交，故同一 expectedRevision 不会被并发提交。
  const inFlightRef = useRef<Set<string>>(new Set());
  // timersRef：每页一个待提交定时器（切页不再清掉原页的定时器）。
  const timersRef = useRef<Map<string, number>>(new Map());
  // editSeqRef：本地编辑序号（全局单调）。提交发出时记下，返回时比对即可判断"在途期间该页又有新编辑"。
  const editSeqRef = useRef(0);
  // displayedSlideIdRef：当前显示的页（render 期同步）—— 回调/定时器据它判断"该页是否仍在前台"，
  // 只有前台的页才允许改 saveState 与组合态。
  const displayedSlideIdRef = useRef(script.slideId);
  displayedSlideIdRef.current = script.slideId;
  // appliedSlideIdRef：本地 state 当前对应的页（在 reset effect 内更新），用于识别"切页"。
  const appliedSlideIdRef = useRef(script.slideId);
  const caretRef = useRef<number>(0);
  const saveStateRef = useRef<ScriptEditorStatus>(saveState);
  saveStateRef.current = saveState;
  // 每页最后已知 revision 取 max 单调更新：服务端 revision 只增不减，而"提交已成功但父级尚未重渲染"
  // 的中间窗口若把 expectedRevision 写回旧值，下一次提交会自己撞 conflict。
  const knownRevision = revisionsRef.current.get(script.slideId);
  revisionsRef.current.set(script.slideId, knownRevision === undefined ? script.revision : Math.max(knownRevision, script.revision));

  // 仅在切换页面或服务器落库（revision 变化）时重置文本，避免每次按键被父级 onChange 回写冲刷光标。
  // R-13：这两件事必须分开处理 —— 切页必须重置；服务端落库不能无条件覆盖本地文本。
  // 旧实现无条件 `setTexts(initialTexts(script))` + `setSaveState('saved')`：提交在途时用户继续输入的编辑
  // 会在提交成功回调推进 revision 后被服务端文本覆盖，且保存状态被置回 saved，**在途编辑静默丢失**。
  useEffect(() => {
    const switchedSlide = appliedSlideIdRef.current !== script.slideId;
    appliedSlideIdRef.current = script.slideId;
    // 服务端落库（revision 推进）时本页仍有未落库草案 → 保留本地文本，交给草案自身继续推进 revision。
    // （冲突时草案已被放弃，故仍走下面的对齐分支 —— 对照面板的「采用服务端版本」正依赖这次对齐。）
    if (!switchedSlide && draftsRef.current.has(script.slideId)) return;
    setTexts(initialTexts(script));
    setSaveState('saved');
    setRewriteNote(null);
    if (switchedSlide) {
      // 切页：丢弃组合态与选择集（新页一切从头开始）。
      // 注意**不能**清原页的待提交定时器与草案 —— 那是原页未落库的编辑，必须继续提交。
      composingRef.current = false;
      setSelected(new Set());
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [script.slideId, script.revision]);

  // 向父组件上报保存状态（用于未保存提示 / Ctrl+S 指示）。
  useEffect(() => {
    onStatusChange?.(saveState);
  }, [saveState, onStatusChange]);

  // segmentsWithTexts：把本地文本投影回段落（草案快照与提交内容共用同一投影）。
  const segmentsWithTexts = (source: Record<string, string>): ScriptSegment[] =>
    script.segments.map((segment) => ({
      ...segment,
      displayText: source[segment.segmentId] ?? segment.displayText,
      spokenText: source[segment.segmentId] ?? segment.spokenText
    }));

  // commitDraft：把**指定页**的未落库草案提交到服务端（可提交非当前显示页 —— R-13 的关键）。
  const commitDraft = (slideId: string) => {
    const draft = draftsRef.current.get(slideId);
    if (!draft) return;
    const expected = revisionsRef.current.get(slideId) ?? script.revision;
    // 只有"仍在前台"的页才允许改 saveState —— 否则切页后的回包会把新页状态改乱
    // （旧实现正是这样把新页置回 dirty，并顺带发起一次针对新页的多余提交）。
    const onShown = (fn: () => void) => {
      if (slideId === displayedSlideIdRef.current) fn();
    };

    if (!commit) {
      // 本地模式（无提交通道）：沿用原语义 —— 直接推进 revision 并把文本交回父级。
      const next: ScriptRevision = { ...script, slideId, revision: expected + 1, segments: draft.segments };
      revisionsRef.current.set(slideId, next.revision);
      draftsRef.current.delete(slideId);
      onChange(next);
      onShown(() => setSaveState('saved'));
      return;
    }

    onShown(() => setSaveState('saving'));
    inFlightRef.current.add(slideId);
    const seq = draft.seq;
    commit(slideId, draft.segments, expected)
      .then((saved) => {
        inFlightRef.current.delete(slideId);
        revisionsRef.current.set(slideId, Math.max(revisionsRef.current.get(slideId) ?? 0, saved.revision));
        onChange(saved);
        const latest = draftsRef.current.get(slideId);
        if (latest && latest.seq !== seq) {
          // 在途期间该页又有新编辑（用户可能已切走）：草案保留，按新 revision 立即重排续传。
          onShown(() => setSaveState('dirty'));
          scheduleSaveRef.current(slideId, 0);
          return;
        }
        draftsRef.current.delete(slideId);
        onShown(() => setSaveState('saved'));
      })
      .catch((error) => {
        inFlightRef.current.delete(slideId);
        // 冲突：本地文本已由父级存入 conflict.localText，两条出口（采用服务端版本 / 以最新版本重试）
        // 都在父级 —— 编辑器必须放弃草案，否则会把用户明确放弃的本地文本再写回去。
        if (error instanceof ScriptConflictError) draftsRef.current.delete(slideId);
        onShown(() => setSaveState('error'));
        onCommitError?.(error instanceof Error ? error.message : t('script.saveFailed'));
      });
  };

  // commitDraft 每次渲染重建，存入 ref 供 flush / 保存调度器取用最新闭包。
  const commitDraftRef = useRef(commitDraft);
  commitDraftRef.current = commitDraft;

  // A10：自动保存只有一个调度入口，但**按页各保留一个待提交定时器**。
  // 原实现里"防抖 effect 的定时器"与"组合结束另起的定时器"互相独立、彼此不可见，
  // 中文输入法确认时会并发两次提交（同一 expectedRevision）→ 服务端冲突/重复写。
  // 现在同一页只有一个定时器，任何触发点都先清掉该页上一个待提交任务。
  const scheduleSaveRef = useRef<(slideId: string, delay?: number) => void>(() => {});
  const scheduleSave = (slideId: string, delay = SAVE_DEBOUNCE_MS) => {
    const pending = timersRef.current.get(slideId);
    if (pending !== undefined) window.clearTimeout(pending);
    timersRef.current.set(
      slideId,
      window.setTimeout(() => {
        timersRef.current.delete(slideId);
        // 组合期绝不提交半成品（A10），且这只对"仍在编辑的那一页"成立 —— 已切走的页不再有组合态；
        // 此处不重排，由 onCompositionEnd 统一补一次。
        if (slideId === displayedSlideIdRef.current && composingRef.current) return;
        // 该页已有提交在途：本次编辑不能丢，退避后重排（等 revision 跟上再提交，避免并发撞 conflict）。
        if (inFlightRef.current.has(slideId)) {
          scheduleSaveRef.current(slideId, SAVE_RETRY_MS);
          return;
        }
        // "是否需要提交"以草案是否存在为准（旧实现看 saveState === 'dirty'，会被跨页回包误置）；
        // 无草案即无需提交，error 仍保持人工重试语义（原行为不变）。
        commitDraftRef.current(slideId);
      }, delay)
    );
  };
  scheduleSaveRef.current = scheduleSave;

  useImperativeHandle(
    ref,
    () => ({
      flush: () => {
        // 逐页提交所有未落库草案（不只当前页）—— 切页后原页的草案必须在这里续上，
        // 否则「切换页面前先提交」这条承诺对原页失效。
        draftsRef.current.forEach((_draft, slideId) => {
          // A10：当前页组合未结束（拼音候选未确认）不强制提交，避免把半成品写进讲稿；
          // 组合结束后 onCompositionEnd 会自动补一次提交。
          if (slideId === displayedSlideIdRef.current && composingRef.current) return;
          // 该页已有提交在途：不并发发起第二次提交（同一 expectedRevision 必冲突），交给调度器退避重排。
          if (inFlightRef.current.has(slideId)) {
            scheduleSaveRef.current(slideId, SAVE_RETRY_MS);
            return;
          }
          const pending = timersRef.current.get(slideId);
          if (pending !== undefined) {
            window.clearTimeout(pending);
            timersRef.current.delete(slideId);
          }
          commitDraftRef.current(slideId);
        });
      },
      isDirty: () =>
        draftsRef.current.size > 0 ||
        saveStateRef.current === 'dirty' ||
        saveStateRef.current === 'saving' ||
        saveStateRef.current === 'error'
    }),
    []
  );

  const editSegment = (segmentId: string, value: string) => {
    // R-13：编辑序号用于让在途提交的返回结果识别"已有更新编辑"（不置回 saved、不覆盖本地文本）。
    editSeqRef.current += 1;
    const seq = editSeqRef.current;
    const slideId = script.slideId;
    setTexts((current) => {
      const next = { ...current, [segmentId]: value };
      // 草案快照必须在这里落表：切页后 texts 会被新页内容覆盖，原页未落库的文本只能靠这份
      // 快照续传到服务端（否则"提交在途期间对原页的编辑"会随切页永久丢失）。
      draftsRef.current.set(slideId, { segments: segmentsWithTexts(next), seq });
      return next;
    });
    setSaveState('dirty');
    onEdited?.();
    // A10：中文输入法组合期间（拼音串/候选未确认）不调度保存，待 onCompositionEnd 再落库。
    if (composingRef.current) return;
    scheduleSaveRef.current(slideId);
  };

  const toggleSelect = (segmentId: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(segmentId)) next.delete(segmentId);
      else next.add(segmentId);
      return next;
    });
  };

  // 工具栏目标段落：显式选中的优先；否则取当前聚焦段。
  const targetIds = (): string[] => {
    if (selected.size > 0) return [...selected];
    if (focusedId) return [focusedId];
    return [];
  };

  // 段落改写（缩短/润色/衔接）：逐段串行调用 LLM（避免突发顶爆限流），成功即经本地草稿
  // 自动保存落库并点亮"语音待更新"；进行中整条工具栏禁用防止重复频繁点击。
  const handleRewrite = async (action: RewriteAction) => {
    if (rewritingRef.current || !onRewriteText) return;
    const slideId = script.slideId;
    const ids = targetIds();
    if (ids.length === 0) return;
    rewritingRef.current = true;
    setRewritingSegs(new Set(ids));
    setRewriteNote(null);
    let changed = 0;
    let stayed = 0;
    try {
      for (const id of ids) {
        // 改写过程用户切页：中止后续段落，避免把别页状态写坏。
        if (displayedSlideIdRef.current !== slideId) break;
        const src = (texts[id] ?? '').trim();
        if (!src) {
          stayed++;
          continue;
        }
        try {
          const next = (await onRewriteText(src, action)).trim();
          if (next && next !== src && displayedSlideIdRef.current === slideId) {
            editSegment(id, next);
            changed++;
          } else {
            stayed++; // 模型返回原文（可能已很精炼）：不静默，在回执里说明。
          }
        } catch (err) {
          if (displayedSlideIdRef.current === slideId) {
            setRewriteNote(null);
            onRewriteError?.(err instanceof Error ? err.message : String(err));
          }
          break;
        }
      }
    } finally {
      rewritingRef.current = false;
      setRewritingSegs(new Set());
      if (displayedSlideIdRef.current === slideId) {
        if (changed > 0 && stayed > 0) setRewriteNote(t('editor.rewritePartial', { changed, stayed }));
        else if (changed > 0) setRewriteNote(t('editor.rewriteDone', { count: changed }));
        else setRewriteNote(t('editor.rewriteNoChange'));
      }
    }
  };

  // M4 ⑤：打开读音调整 popover（预填当前目标段文本为原词，影响范围实时估算）。
  const openPronounce = () => {
    const ids = targetIds();
    if (ids.length === 0) return;
    const base = texts[ids[0]] ?? '';
    setPopoverWord(base);
    setPopoverReading('');
    setAffectedCount(0);
    setPopoverOpen(true);
  };

  // 在目标段光标处插入朗读标记（发音/停顿）。
  const insertMarker = (marker: string) => {
    const ids = targetIds();
    if (ids.length === 0) return;
    const id = ids[0];
    const el = textareaRefs.current[id];
    const base = texts[id] ?? '';
    const pos = el ? el.selectionStart : base.length;
    const next = base.slice(0, pos) + marker + base.slice(pos);
    editSegment(id, next);
    requestAnimationFrame(() => {
      const fresh = textareaRefs.current[id];
      if (fresh) {
        fresh.focus();
        const caret = pos + marker.length;
        fresh.setSelectionRange(caret, caret);
      }
    });
  };

  const anchors = script.segments.flatMap((segment) => segment.sourceAnchors ?? []);
  const visualCount = anchors.filter((anchor) => anchor.kind.startsWith('visual_')).length;
  // 来源锚点只展示类型（备注/标题/内容/表格…），不展示具体内容。
  const anchorTypeLabel = (kind: string): string => {
    if (kind.startsWith('visual_')) return t('script.anchorType.image');
    const bare = kind.replace(/^shape_/, '').toLowerCase();
    switch (bare) {
      case 'notes':
        return t('script.anchorType.notes');
      case 'title':
        return t('script.anchorType.title');
      case 'body':
      case 'text':
      case 'textbox':
      case 'placeholder':
        return t('script.anchorType.body');
      case 'table':
        return t('script.anchorType.table');
      case 'chart':
        return t('script.anchorType.chart');
      case 'picture':
      case 'image':
        return t('script.anchorType.image');
      case 'autoshape':
      case 'shape':
      case 'freeform':
        return t('script.anchorType.shape');
      default:
        return t('script.anchorType.other');
    }
  };
  const anchorTypes = [...new Set(anchors.map((anchor) => anchorTypeLabel(anchor.kind)))];

  return (
    <section className="editor-card script-editor" aria-label={t('script.aria')}>
      <header>
        <div>
          <span className="eyebrow">{t('script.currentSlide')}</span>
          <h2>{slideTitle || script.slideId}</h2>
        </div>
        <div className="script-header-right">
          <span className={`save-state ${saveState}`}>
            {saveState === 'saved'
              ? t('script.saved')
              : saveState === 'saving'
                ? t('script.saving')
                : saveState === 'error'
                  ? t('script.saveError')
                  : saveState === 'conflict'
                    ? t('script.conflict')
                    : t('script.dirty')}
          </span>
        </div>
      </header>

      {/* B4-M1：只读说明（Viewer/Reviewer 可审阅但不可改稿，服务端 Update 要求 EDITOR）。 */}
      {!canEdit && <p className="perm-hint">{t('script.readOnlyNote')}</p>}

      {/* 段落工具栏：缩短/润色/衔接 → LLM 改写选中/当前段落（串行防突发）并本地落库；
          发音/停顿 → 插入朗读标记。改写进行中整条禁用，防止重复频繁点击产生重复 LLM 调用。
          B4-M1：无编辑权限（EDITOR 以下）时整体禁用，避免出现必然 403 的假能力（A22）。 */}
      <div className="paragraph-toolbar" role="toolbar" aria-label={t('editor.toolbar')}>
        <button type="button" disabled={!canEdit || !onRewriteText || rewritingRef.current || targetIds().length === 0} onClick={() => handleRewrite('shorten')} title={t('editor.shortenHint')}>
          {t('editor.shorten')}
        </button>
        <button type="button" disabled={!canEdit || !onRewriteText || rewritingRef.current || targetIds().length === 0} onClick={() => handleRewrite('polish')} title={t('editor.polishHint')}>
          {t('editor.polish')}
        </button>
        <button type="button" disabled={!canEdit || !onRewriteText || rewritingRef.current || targetIds().length === 0} onClick={() => handleRewrite('transition')} title={t('editor.transitionHint')}>
          {t('editor.transition')}
        </button>
        <span className="toolbar-sep" />
        <button type="button" disabled={!canEdit || rewritingRef.current || targetIds().length === 0} onClick={openPronounce} title={t('editor.pronounceHint')}>
          {t('editor.pronounce')}
        </button>
        <button type="button" disabled={!canEdit || rewritingRef.current || targetIds().length === 0} onClick={() => insertMarker(PAUSE_MARKER)} title={t('editor.pauseHint')}>
          {t('editor.pause')}
        </button>
        {selected.size > 0 && (
          <button type="button" className="toolbar-clear" onClick={() => setSelected(new Set())}>
            {t('editor.clearSelection', { count: selected.size })}
          </button>
        )}
      </div>
      {rewritingRef.current && <p className="rewriting-hint">{t('editor.rewritingHint', { count: rewritingSegs.size })}</p>}
      {rewriteNote && !rewritingRef.current && (
        <p className={`rewrite-note ${rewriteNote === t('editor.rewriteNoChange') ? 'stale' : ''}`}>
          {rewriteNote}
          <button type="button" className="note-dismiss" onClick={() => setRewriteNote(null)} aria-label={t('common.close')}>
            ×
          </button>
        </p>
      )}

      {/* M4 ⑤ 读音调整 popover：原词 + 读音 + 本处插入 / 添加到租户词典 + 影响范围回执。 */}
      {popoverOpen && (
        <div className="pronounce-popover" role="dialog" aria-modal="true" aria-label={t('editor.pronouncePopover')} ref={popoverRef}>
          <label className="field-label">
            {t('editor.pronounceWord')}
            <input
              value={popoverWord}
              onChange={(e) => {
                const word = e.currentTarget.value;
                setPopoverWord(word);
                setAffectedCount(word ? script.segments.filter((s) => (texts[s.segmentId] ?? '').includes(word)).length : 0);
              }}
            />
          </label>
          <label className="field-label">
            {t('editor.pronounceReading')}
            <input value={popoverReading} onChange={(e) => setPopoverReading(e.currentTarget.value)} placeholder={t('editor.pronounceReadingPlaceholder')} />
          </label>
          <div className="popover-actions">
            <button type="button" disabled={!popoverReading} onClick={() => { insertMarker(`〔读：${popoverReading}〕`); setPopoverOpen(false); }}>
              {t('editor.insertHere')}
            </button>
            <button type="button" disabled={!popoverWord || !popoverReading} onClick={() => { onAddToDictionary?.(popoverWord, popoverReading); setPopoverOpen(false); }}>
              {t('editor.addToDictionary')}
            </button>
          </div>
          {popoverWord && (
            <p className="popover-hint">{t('editor.affectedSegments', { count: affectedCount })}</p>
          )}
        </div>
      )}

      {/* 分段编辑（M3 ②）：每段独立卡片，可勾选、独立状态徽标。 */}
      <div className="segment-list">
        {script.segments.map((segment, index) => {
          const segRewrite = rewritingSegs.has(segment.segmentId);
          return (
            <article key={segment.segmentId} className={`segment-card ${selected.has(segment.segmentId) ? 'selected' : ''} ${segRewrite ? 'regenerating' : ''}`}>
              <label className="segment-head">
                <input
                  type="checkbox"
                  checked={selected.has(segment.segmentId)}
                  disabled={!canEdit || segRewrite || rewritingRef.current}
                  onChange={() => toggleSelect(segment.segmentId)}
                />
                <span className="segment-index">{index + 1}</span>
                {segRewrite && <span className="seg-badge regen">{t('editor.rewriting')}</span>}
              </label>
              <textarea
                ref={(el) => {
                  textareaRefs.current[segment.segmentId] = el;
                }}
                value={texts[segment.segmentId] ?? ''}
                readOnly={!canEdit || segRewrite}
                onFocus={() => setFocusedId(segment.segmentId)}
                onClick={(e) => {
                  setFocusedId(segment.segmentId);
                  caretRef.current = (e.currentTarget as HTMLTextAreaElement).selectionStart;
                }}
                onKeyUp={(e) => {
                  caretRef.current = (e.currentTarget as HTMLTextAreaElement).selectionStart;
                }}
                onSelect={(e) => {
                  caretRef.current = (e.currentTarget as HTMLTextAreaElement).selectionStart;
                }}
                onChange={(event) => editSegment(segment.segmentId, event.currentTarget.value)}
                onCompositionStart={() => {
                  composingRef.current = true;
                }}
                onCompositionEnd={() => {
                  composingRef.current = false;
                  // 组合确认后文本才算最终：补齐组合期被跳过的调度（沿用统一防抖窗口，连续输入只提交一次）。
                  if (saveStateRef.current === 'dirty') scheduleSaveRef.current(script.slideId);
                }}
              />
            </article>
          );
        })}
      </div>

      {/* 讲稿随时可编辑、自动保存。
          操作顺序：先「重新生成讲稿」（选模式），改动后再出现「重新生成语音」。 */}
      <footer className="review-actions">
        <div className="script-regen" ref={modeMenuRef}>
          <div className="script-regen-split">
            <button
              type="button"
              className="button-ghost script-regen-main"
              disabled={!canEdit || scriptBusy || !onRegenerateScript}
              onClick={() => onRegenerateScript?.(lastRegenMode)}
              title={t('editor.regenerateScriptWith', { mode: t(SCRIPT_REGEN_MODES.find((m) => m.value === lastRegenMode)?.labelKey ?? 'editor.modes.polish') })}
            >
              {scriptBusy ? t('editor.scriptRegenerating') : t('editor.regenerateScript')}
            </button>
            <button
              type="button"
              className="button-ghost script-regen-caret"
              disabled={!canEdit || scriptBusy || !onRegenerateScript}
              onClick={() => setModeMenuOpen((value) => !value)}
              aria-haspopup="menu"
              aria-expanded={modeMenuOpen}
              aria-label={t('editor.regenerateScriptMode')}
              title={t('editor.regenerateScriptMode')}
            >
              <span aria-hidden="true">▾</span>
            </button>
          </div>
          {modeMenuOpen && (
            <div className="script-mode-menu" role="menu">
              {SCRIPT_REGEN_MODES.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    setModeMenuOpen(false);
                    setLastRegenMode(option.value);
                    onRegenerateScript?.(option.value);
                  }}
                >
                  <strong>{t(option.labelKey)}</strong>
                  <small>{t(option.descKey)}</small>
                </button>
              ))}
            </div>
          )}
          {scriptBusy ? (
            <span className="script-regen-status">{scriptStatusText ?? t('editor.scriptRegenerating')}</span>
          ) : (
            scriptStatusText && <span className={`script-regen-status${scriptError ? ' error' : ''}`}>{scriptStatusText}</span>
          )}
        </div>

        {(voiceNeedsUpdate || voiceBusy) && (
          <button
            type="button"
            className="button-primary"
            disabled={!canEdit || voiceBusy || !onRegenerateVoice}
            onClick={() => onRegenerateVoice?.()}
          >
            {voiceBusy ? t('editor.voiceGenerating') : t('editor.regenerateVoice')}
          </button>
        )}
        {voiceBusy ? (
          <div className="voice-progress" role="status" aria-live="polite">
            <span className="voice-progress-track">
              <span className="voice-progress-fill" style={{ width: `${voiceProgress >= 0 ? Math.min(100, Math.max(0, voiceProgress)) : 0}%` }} />
            </span>
            <span className="voice-progress-text">
              {voiceStatusText ?? t('editor.voiceProgressRunning')}
              {voiceProgress >= 0 ? ` ${Math.round(voiceProgress)}%` : '…'}
            </span>
          </div>
        ) : (
          voiceNeedsUpdate && !voiceBusy && <span className="warn-note">{t('editor.voiceNeedsUpdate')}</span>
        )}
      </footer>

      {anchors.length > 0 && (
        <div className="anchor-strip" aria-label={t('script.currentSlide')}>
          <strong>{t('script.anchors', { count: anchors.length })}</strong>
          <span>{visualCount > 0 ? t('script.visualAnchors', { count: visualCount }) : t('script.structuralAnchors')}</span>
          {anchorTypes.map((label) => (
            <em key={label}>{label}</em>
          ))}
        </div>
      )}
    </section>
  );
});

function initialTexts(script: ScriptRevision): Record<string, string> {
  const out: Record<string, string> = {};
  for (const segment of script.segments) out[segment.segmentId] = segment.displayText;
  return out;
}
