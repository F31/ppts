import { useEffect, useState } from 'react';
import type { ScriptRevision, ScriptSegment } from './types';

type Props = {
  script: ScriptRevision;
  onChange: (script: ScriptRevision) => void;
  commit?: (segments: ScriptSegment[], expectedRevision: number) => Promise<ScriptRevision>;
  onCommitError?: (message: string) => void;
};

export function ScriptEditor({ script, onChange, commit, onCommitError }: Props) {
  const [draft, setDraft] = useState(script.segments.map((segment) => segment.displayText).join('\n\n'));
  const [saveState, setSaveState] = useState<'saved' | 'dirty' | 'saving'>('saved');
  const anchors = script.segments.flatMap((segment) => segment.sourceAnchors ?? []);
  const visualCount = anchors.filter((anchor) => anchor.kind.startsWith('visual_')).length;

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

  useEffect(() => {
    if (saveState !== 'dirty') return;
    const timer = window.setTimeout(() => {
      setSaveState('saving');
      window.setTimeout(() => {
        const segments = splitSegments();
        if (!commit) {
          onChange({
            ...script,
            revision: script.revision + 1,
            segments
          });
          setSaveState('saved');
          return;
        }
        commit(segments, script.revision)
          .then((saved) => {
            onChange(saved);
            setSaveState('saved');
          })
          .catch((error) => {
            setSaveState('saved');
            onCommitError?.(error instanceof Error ? error.message : '讲稿保存失败');
          });
      }, 220);
    }, 500);
    return () => window.clearTimeout(timer);
  }, [draft, onChange, commit, saveState, script]);

  return (
    <section className="editor-card" aria-label="讲稿编辑">
      <header>
        <div>
          <span className="eyebrow">当前页讲稿</span>
          <h2>{script.slideId}</h2>
        </div>
        <span className={`save-state ${saveState}`}>{saveState === 'saved' ? '已保存' : saveState === 'saving' ? '保存中' : '未保存'}</span>
      </header>
      <textarea
        value={draft}
        readOnly={script.status === 'locked'}
        onChange={(event) => {
          setDraft(event.currentTarget.value);
          setSaveState('dirty');
        }}
      />
      <footer>
        <span>revision {script.revision} · {modeLabel(script.mode)}</span>
        <span>{script.status === 'locked' ? '已锁定' : script.status === 'approved' ? '已审核' : '草稿'}</span>
      </footer>
      {anchors.length > 0 && (
        <div className="anchor-strip" aria-label="来源锚点">
          <strong>{anchors.length} 个来源锚点</strong>
          <span>{visualCount > 0 ? `含 ${visualCount} 个视觉锚点` : '结构锚点'}</span>
          {anchors.slice(0, 3).map((anchor, index) => (
            <em key={`${anchor.slideId}-${anchor.shapeId}-${index}`}>
              {anchor.kind.startsWith('visual_') ? '视觉' : '结构'} {Math.round(anchor.confidence * 100)}% · {anchor.raw}
            </em>
          ))}
        </div>
      )}
    </section>
  );
}

function modeLabel(mode: ScriptRevision['mode']) {
  switch (mode) {
    case 'SCRIPT_MODE_POLISH':
      return '润色讲解';
    case 'SCRIPT_MODE_AI_GENERATED':
      return 'AI 生成讲解';
    default:
      return '原文朗读';
  }
}
