import { useEffect, useState } from 'react';
import type { ScriptRevision } from './types';

type Props = {
  script: ScriptRevision;
  onChange: (script: ScriptRevision) => void;
};

export function ScriptEditor({ script, onChange }: Props) {
  const [draft, setDraft] = useState(script.segments.map((segment) => segment.displayText).join('\n\n'));
  const [saveState, setSaveState] = useState<'saved' | 'dirty' | 'saving'>('saved');

  useEffect(() => {
    setDraft(script.segments.map((segment) => segment.displayText).join('\n\n'));
    setSaveState('saved');
  }, [script.slideId, script.revision, script.segments]);

  useEffect(() => {
    if (saveState !== 'dirty') return;
    const timer = window.setTimeout(() => {
      setSaveState('saving');
      window.setTimeout(() => {
        onChange({
          ...script,
          revision: script.revision + 1,
          segments: script.segments.map((segment, index) => ({
            ...segment,
            displayText: draft.split(/\n{2,}/)[index] ?? draft,
            spokenText: draft.split(/\n{2,}/)[index] ?? draft
          }))
        });
        setSaveState('saved');
      }, 220);
    }, 500);
    return () => window.clearTimeout(timer);
  }, [draft, onChange, saveState, script]);

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
        <span>revision {script.revision}</span>
        <span>{script.status === 'locked' ? '已锁定' : script.status === 'approved' ? '已审核' : '草稿'}</span>
      </footer>
    </section>
  );
}
