import { useEffect, useState } from 'react';
import { useI18n } from './i18n';
import type { ScriptRevision, ScriptSegment } from './types';

type Props = {
  script: ScriptRevision;
  onChange: (script: ScriptRevision) => void;
  commit?: (segments: ScriptSegment[], expectedRevision: number) => Promise<ScriptRevision>;
  onCommitError?: (message: string) => void;
};

export function ScriptEditor({ script, onChange, commit, onCommitError }: Props) {
  const { t } = useI18n();
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
            onCommitError?.(error instanceof Error ? error.message : t('script.saveFailed'));
          });
      }, 220);
    }, 500);
    return () => window.clearTimeout(timer);
  }, [draft, onChange, commit, saveState, script]);

  return (
    <section className="editor-card" aria-label={t('script.aria')}>
      <header>
        <div>
          <span className="eyebrow">{t('script.currentSlide')}</span>
          <h2>{script.slideId}</h2>
        </div>
        <span className={`save-state ${saveState}`}>{saveState === 'saved' ? t('script.saved') : saveState === 'saving' ? t('script.saving') : t('script.dirty')}</span>
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
}

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
