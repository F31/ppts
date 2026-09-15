import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react';
import { useI18n } from './i18n';
import type { ScriptRevision, ScriptSegment } from './types';

export type ScriptEditorStatus = 'saved' | 'dirty' | 'saving' | 'error' | 'conflict';

export type ScriptEditorHandle = {
  // flush：立即提交当前待保存草稿（Ctrl+S / 切换页面前调用）。
  flush: () => void;
  // isDirty：存在尚未落库的编辑（dirty/saving/error 均视为未保存）。
  isDirty: () => boolean;
};

type Props = {
  script: ScriptRevision;
  onChange: (script: ScriptRevision) => void;
  commit?: (segments: ScriptSegment[], expectedRevision: number) => Promise<ScriptRevision>;
  onCommitError?: (message: string) => void;
  onStatusChange?: (status: ScriptEditorStatus) => void;
  // canReview：当前用户具 REVIEWER 及以上角色时显示确认/锁定按钮。
  canReview?: boolean;
  // canEdit：当前用户具 EDITOR 及以上角色（服务端 ScriptService.Update 的最低要求，script.go:55）。
  // 为 false 时段落只读、工具栏禁用，避免 Viewer/Reviewer 看到可写却必然 403 的假能力（A22）。
  canEdit?: boolean;
  onApprove?: () => void;
  onLock?: () => void;
  // regeneratingIds：后端正在局部重生成的段落（显示占位、禁用编辑）。
  regeneratingIds?: string[];
  // onRegenerate：段落工具栏"缩短/润色/衔接"触发（M1 RegenerateSegments）。
  onRegenerate?: (segmentIds: string[]) => void;
  // onAddToDictionary：M4 ⑤ 读音调整弹窗"添加到租户词典"（pattern=原词, replacement=读音）。
  onAddToDictionary?: (word: string, reading: string) => void;
};

// 停顿标记：插入到 spokenText 的朗读提示；发音由 M4 ⑤ 读音 popover 经 〔读：x〕 标记处理。
const PAUSE_MARKER = '‖';

export const ScriptEditor = forwardRef<ScriptEditorHandle, Props>(function ScriptEditor(
  { script, onChange, commit, onCommitError, onStatusChange, canReview, canEdit = true, onApprove, onLock, regeneratingIds, onRegenerate, onAddToDictionary },
  ref
) {
  const { t } = useI18n();
  // 各分段展示文本（display==spoken，编辑同步）。
  const [texts, setTexts] = useState<Record<string, string>>(() => initialTexts(script));
  const [saveState, setSaveState] = useState<ScriptEditorStatus>('saved');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focusedId, setFocusedId] = useState<string | null>(script.segments[0]?.segmentId ?? null);
  // M4 ⑤ 读音调整 popover 状态。
  const [popoverOpen, setPopoverOpen] = useState(false);
  const [popoverWord, setPopoverWord] = useState('');
  const [popoverReading, setPopoverReading] = useState('');
  const [affectedCount, setAffectedCount] = useState(0);
  const composingRef = useRef(false);
  const timerRef = useRef<number | undefined>(undefined);
  const textareaRefs = useRef<Record<string, HTMLTextAreaElement | null>>({});
  const caretRef = useRef<number>(0);
  const saveStateRef = useRef<ScriptEditorStatus>(saveState);
  saveStateRef.current = saveState;

  // 仅在切换页面或服务器落库（revision 变化）时重置文本，避免每次按键被父级 onChange 回写冲刷光标。
  useEffect(() => {
    setTexts(initialTexts(script));
    setSaveState('saved');
    setSelected(new Set());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [script.slideId, script.revision]);

  // 向父组件上报保存状态（用于未保存提示 / Ctrl+S 指示）。
  useEffect(() => {
    onStatusChange?.(saveState);
  }, [saveState, onStatusChange]);

  const buildSegments = (): ScriptSegment[] =>
    script.segments.map((segment) => ({
      ...segment,
      displayText: texts[segment.segmentId] ?? segment.displayText,
      spokenText: texts[segment.segmentId] ?? segment.spokenText
    }));

  const runCommit = () => {
    const segments = buildSegments();
    setSaveState('saving');
    if (!commit) {
      onChange({ ...script, revision: script.revision + 1, segments });
      setSaveState('saved');
      return;
    }
    commit(segments, script.revision)
      .then((saved) => {
        onChange(saved);
        setSaveState('saved');
      })
      .catch((error) => {
        setSaveState('error');
        onCommitError?.(error instanceof Error ? error.message : t('script.saveFailed'));
      });
  };

  // runCommit 每次渲染重建，存入 ref 供 flush / 组合结束回调取用最新闭包。
  const runCommitRef = useRef(runCommit);
  runCommitRef.current = runCommit;

  // 防抖自动保存（500ms + 220ms）；组合输入期间不调度，待组合结束再提交。
  useEffect(() => {
    if (saveState !== 'dirty') return;
    const timer = window.setTimeout(() => {
      if (composingRef.current) return;
      timerRef.current = window.setTimeout(() => runCommitRef.current(), 220);
    }, 500);
    return () => window.clearTimeout(timer);
  }, [texts, saveState]);

  useImperativeHandle(
    ref,
    () => ({
      flush: () => {
        if (saveStateRef.current === 'dirty' || saveStateRef.current === 'error') {
          window.clearTimeout(timerRef.current);
          runCommitRef.current();
        }
      },
      isDirty: () => saveStateRef.current === 'dirty' || saveStateRef.current === 'saving' || saveStateRef.current === 'error'
    }),
    []
  );

  const editSegment = (segmentId: string, value: string) => {
    setTexts((current) => ({ ...current, [segmentId]: value }));
    setSaveState('dirty');
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

  const handleRegenerate = () => {
    const ids = targetIds().filter((id) => !(regeneratingIds ?? []).includes(id));
    if (ids.length === 0 || !onRegenerate) return;
    onRegenerate(ids);
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

  const locked = script.status === 'locked';
  const regenSet = new Set(regeneratingIds ?? []);
  const anchors = script.segments.flatMap((segment) => segment.sourceAnchors ?? []);
  const visualCount = anchors.filter((anchor) => anchor.kind.startsWith('visual_')).length;

  return (
    <section className="editor-card script-editor" aria-label={t('script.aria')}>
      <header>
        <div>
          <span className="eyebrow">{t('script.currentSlide')}</span>
          <h2>{script.slideId}</h2>
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
          <span className={`status-marker ${script.status}`}>
            {script.status === 'locked' ? t('editor.locked') : script.status === 'approved' ? t('editor.approved') : t('editor.draft')}
          </span>
        </div>
      </header>

      {/* B4-M1：只读说明（Viewer/Reviewer 可审阅但不可改稿，服务端 Update 要求 EDITOR）。 */}
      {!canEdit && <p className="perm-hint">{t('script.readOnlyNote')}</p>}

      {/* 段落工具栏（M3 ②）：缩短/润色/衔接 → 局部重生成；发音/停顿 → 插入朗读标记。
          B4-M1：无编辑权限（EDITOR 以下）时整体禁用，避免出现必然 403 的假能力（A22）。 */}
      <div className="paragraph-toolbar" role="toolbar" aria-label={t('editor.toolbar')}>
        <button type="button" disabled={!canEdit || targetIds().length === 0} onClick={handleRegenerate} title={t('editor.shortenHint')}>
          {t('editor.shorten')}
        </button>
        <button type="button" disabled={!canEdit || targetIds().length === 0} onClick={handleRegenerate} title={t('editor.polishHint')}>
          {t('editor.polish')}
        </button>
        <button type="button" disabled={!canEdit || targetIds().length === 0} onClick={handleRegenerate} title={t('editor.transitionHint')}>
          {t('editor.transition')}
        </button>
        <span className="toolbar-sep" />
        <button type="button" disabled={!canEdit || targetIds().length === 0 || locked} onClick={openPronounce} title={t('editor.pronounceHint')}>
          {t('editor.pronounce')}
        </button>
        <button type="button" disabled={!canEdit || targetIds().length === 0 || locked} onClick={() => insertMarker(PAUSE_MARKER)} title={t('editor.pauseHint')}>
          {t('editor.pause')}
        </button>
        {selected.size > 0 && (
          <button type="button" className="toolbar-clear" onClick={() => setSelected(new Set())}>
            {t('editor.clearSelection', { count: selected.size })}
          </button>
        )}
      </div>

      {/* M4 ⑤ 读音调整 popover：原词 + 读音 + 本处插入 / 添加到租户词典 + 影响范围回执。 */}
      {popoverOpen && (
        <div className="pronounce-popover" role="dialog" aria-label={t('editor.pronouncePopover')}>
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
          const segRegen = regenSet.has(segment.segmentId);
          return (
            <article key={segment.segmentId} className={`segment-card ${selected.has(segment.segmentId) ? 'selected' : ''} ${segRegen ? 'regenerating' : ''}`}>
              <label className="segment-head">
                <input
                  type="checkbox"
                  checked={selected.has(segment.segmentId)}
                  disabled={!canEdit || segRegen}
                  onChange={() => toggleSelect(segment.segmentId)}
                />
                <span className="segment-index">{index + 1}</span>
                {segment.status === 'approved' && <span className="seg-badge approved">{t('editor.approved')}</span>}
                {segment.status === 'locked' && <span className="seg-badge locked">{t('editor.locked')}</span>}
                {segRegen && <span className="seg-badge regen">{t('editor.regenerating')}</span>}
              </label>
              <textarea
                ref={(el) => {
                  textareaRefs.current[segment.segmentId] = el;
                }}
                value={texts[segment.segmentId] ?? ''}
                readOnly={!canEdit || locked || segRegen}
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
                  if (saveStateRef.current === 'dirty') {
                    window.clearTimeout(timerRef.current);
                    timerRef.current = window.setTimeout(() => runCommitRef.current(), 500 + 220);
                  }
                }}
              />
            </article>
          );
        })}
      </div>

      {/* ③ 确认 / 锁定（需 REVIEWER）。已锁定后编辑只读、锁定按钮禁用。 */}
      {canReview && (
        <footer className="review-actions">
          <button
            type="button"
            className="button-ghost"
            disabled={locked || script.status === 'approved'}
            onClick={() => onApprove?.()}
          >
            {t('editor.approve')}
          </button>
          <button type="button" className="button-ghost" disabled={locked} onClick={() => onLock?.()} title={locked ? t('editor.lockedHint') : ''}>
            {t('editor.lock')}
          </button>
        </footer>
      )}

      {anchors.length > 0 && (
        <div className="anchor-strip" aria-label={t('script.currentSlide')}>
          <strong>{t('script.anchors', { count: anchors.length })}</strong>
          <span>{visualCount > 0 ? t('script.visualAnchors', { count: visualCount }) : t('script.structuralAnchors')}</span>
          {anchors.slice(0, 3).map((anchor, index) => (
            <em key={`${anchor.slideId}-${anchor.shapeId}-${index}`}>
              {anchor.kind.startsWith('visual_') ? t('script.visual') : t('script.structural')} {Math.round(anchor.confidence * 100)}% · {anchor.raw}
            </em>
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
