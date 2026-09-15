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
};

export const ScriptEditor = forwardRef<ScriptEditorHandle, Props>(function ScriptEditor(
  { script, onChange, commit, onCommitError, onStatusChange },
  ref
) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(script.segments.map((segment) => segment.displayText).join('\n\n'));
  const [saveState, setSaveState] = useState<ScriptEditorStatus>('saved');
  const composingRef = useRef(false);
  const timerRef = useRef<number | undefined>(undefined);
  const saveStateRef = useRef<ScriptEditorStatus>(saveState);
  saveStateRef.current = saveState;

  const anchors = script.segments.flatMap((segment) => segment.sourceAnchors ?? []);
  const visualCount = anchors.filter((anchor) => anchor.kind.startsWith('visual_')).length;

  // 向父组件上报保存状态（用于未保存提示 / Ctrl+S 指示）。
  useEffect(() => {
    onStatusChange?.(saveState);
  }, [saveState, onStatusChange]);

  // 切换讲稿时重置草稿与状态。
  useEffect(() => {
    setDraft(script.segments.map((segment) => segment.displayText).join('\n\n'));
    setSaveState('saved');
  }, [script.slideId, script.revision, script.segments]);

  const splitSegments = (): ScriptSegment[] =>
    script.segments.map((segment, index) => ({
      ...segment,
      displayText: draft.split(/\n{2,}/)[index] ?? draft,
      spokenText: draft.split(/\n{2,}/)[index] ?? draft
    }));

  const runCommit = () => {
    const segments = splitSegments();
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
  }, [draft, saveState]);

  useImperativeHandle(
    ref,
    () => ({
      flush: () => {
        // dirty 或 error 都触发提交（error 视为重试保存）。
        if (saveStateRef.current === 'dirty' || saveStateRef.current === 'error') {
          window.clearTimeout(timerRef.current);
          runCommitRef.current();
        }
      },
      isDirty: () => saveStateRef.current === 'dirty' || saveStateRef.current === 'saving' || saveStateRef.current === 'error'
    }),
    []
  );

  return (
    <section className="editor-card" aria-label={t('script.aria')}>
      <header>
        <div>
          <span className="eyebrow">{t('script.currentSlide')}</span>
          <h2>{script.slideId}</h2>
        </div>
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
      </header>
      <textarea
        value={draft}
        readOnly={script.status === 'locked'}
        onChange={(event) => {
          setDraft(event.currentTarget.value);
          setSaveState('dirty');
        }}
        onCompositionStart={() => {
          composingRef.current = true;
        }}
        onCompositionEnd={() => {
          composingRef.current = false;
          // 组合结束且存在未保存内容时，补一次防抖保存（组合期间未调度）。
          if (saveStateRef.current === 'dirty') {
            window.clearTimeout(timerRef.current);
            timerRef.current = window.setTimeout(() => runCommitRef.current(), 500 + 220);
          }
        }}
      />
      <footer>
        <span>{t('script.revision', { revision: script.revision, mode: modeLabel(script.mode, t) })}</span>
        <span>{script.status === 'locked' ? t('script.locked') : script.status === 'approved' ? t('script.approved') : t('script.draft')}</span>
      </footer>
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

function modeLabel(mode: ScriptRevision['mode'], t: (key: string) => string) {
  switch (mode) {
    case 'SCRIPT_MODE_POLISH':
      return t('editor.modes.polish');
    case 'SCRIPT_MODE_AI_GENERATED':
      return t('editor.modes.ai');
    default:
      return t('editor.modes.original');
  }
}
