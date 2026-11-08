import type { Dispatch, RefObject, SetStateAction } from 'react';
import type { ScriptMode } from '../../types';
import type { OneDraftOverwrite } from './oneClickPlan';
import type { Translate } from './model';

export type OneClickSteps = { script: boolean; voice: boolean; animation: boolean };

export type OneClickResult = {
  script: number;
  applied: number;
  skipped: number;
  reasons: string;
  voiceRan: boolean;
};

export type OneClickDrawerProps = {
  dialogRef: RefObject<HTMLElement | null>;
  t: Translate;
  running: boolean;
  steps: OneClickSteps;
  mode: ScriptMode;
  overwrite: OneDraftOverwrite;
  preset: string;
  phase: string;
  error: string;
  done: OneClickResult | null;
  /** 权限闸门：无 script.edit 时不允许发起（仍可查看/勾选，但不替用户越权）。 */
  canRun: boolean;
  onClose: () => void;
  onStepsChange: Dispatch<SetStateAction<OneClickSteps>>;
  onModeChange: (mode: ScriptMode) => void;
  onOverwriteChange: (value: OneDraftOverwrite) => void;
  onPresetChange: (id: string) => void;
  onRun: () => void;
};

/**
 * 一键成片抽屉：讲稿 → 配音 → 动效的唯一入口。
 *
 * 纯展示组件：只发事件、不取数、不持有流水线状态（流水线仍在编辑器，
 * 因为它要串联讲稿生成、配音任务与动效套用三条既有通道）。
 */
export function OneClickDrawer(props: OneClickDrawerProps) {
  const { dialogRef, t, running, steps, mode, overwrite, preset, phase, error, done, canRun } = props;
  const close = () => {
    if (!running) props.onClose();
  };

  return (
    <div className="drawer-backdrop" onClick={close}>
      <aside
        className="properties-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={t('editor.oneClick')}
        onClick={(event) => event.stopPropagation()}
        ref={dialogRef}
      >
        <header>
          <div>
            <span className="eyebrow">{t('editor.oneClickEyebrow')}</span>
            <h2>{t('editor.oneClick')}</h2>
          </div>
          <button
            type="button"
            className="dialog-close-icon"
            disabled={running}
            onClick={props.onClose}
            aria-label={t('common.close')}
            title={t('common.close')}
          >
            ×
          </button>
        </header>
        <section className="panel nested">
          <p className="narration-note">{t('editor.oneClickNote')}</p>
          {/* React 19 合成事件的 currentTarget 在回调结束后会被重置为 null，
              而 setState 的更新函数可能延迟执行：必须先同步取出 checked，
              否则勾选任意一步都会抛 "reading 'checked' of null" 让整页崩掉。 */}
          <div className="one-click-steps">
            <label>
              <input
                type="checkbox"
                checked={steps.script}
                disabled={running}
                onChange={(e) => {
                  const checked = e.currentTarget.checked;
                  props.onStepsChange((s) => ({ ...s, script: checked }));
                }}
              />
              {t('editor.oneClickStepScript')}
            </label>
            <label>
              <input
                type="checkbox"
                checked={steps.voice}
                disabled={running}
                onChange={(e) => {
                  const checked = e.currentTarget.checked;
                  props.onStepsChange((s) => ({ ...s, voice: checked }));
                }}
              />
              {t('editor.oneClickStepVoice')}
            </label>
            <label>
              <input
                type="checkbox"
                checked={steps.animation}
                disabled={running}
                onChange={(e) => {
                  const checked = e.currentTarget.checked;
                  props.onStepsChange((s) => ({ ...s, animation: checked }));
                }}
              />
              {t('editor.oneClickStepAnimation')}
            </label>
          </div>
          <label className="field-label">
            {t('editor.oneDraftMode')}
            <select
              value={mode}
              disabled={running || !steps.script}
              onChange={(e) => props.onModeChange(e.currentTarget.value as ScriptMode)}
            >
              <option value="SCRIPT_MODE_ORIGINAL">{t('editor.mode.original')}</option>
              <option value="SCRIPT_MODE_POLISH">{t('editor.mode.polish')}</option>
              <option value="SCRIPT_MODE_AI_GENERATED">{t('editor.mode.ai')}</option>
            </select>
          </label>
          <label className="field-label">
            {t('editor.oneDraftOverwrite')}
            <select
              value={overwrite}
              disabled={running || !steps.script}
              onChange={(e) => props.onOverwriteChange(e.currentTarget.value as OneDraftOverwrite)}
            >
              <option value="fill_empty">{t('editor.overwriteFillEmpty')}</option>
              <option value="overwrite_all">{t('editor.overwriteAll')}</option>
            </select>
          </label>
          <p className="narration-note">{t('editor.oneClickOverwriteNote')}</p>
          <label className="field-label">
            {t('editor.oneClickPreset')}
            <select value={preset} disabled={running || !steps.animation} onChange={(e) => props.onPresetChange(e.currentTarget.value)}>
              <option value="business-calm">{t('editor.oneClickPresetCalm')}</option>
              <option value="tech-dynamic">{t('editor.oneClickPresetTech')}</option>
              <option value="teaching-clear">{t('editor.oneClickPresetTeaching')}</option>
              <option value="launch-bold">{t('editor.oneClickPresetLaunch')}</option>
            </select>
          </label>
          <p className="narration-note">{t('editor.oneClickAdvancedNote')}</p>
          {phase === 'script' && <p className="narration-note" role="status">{t('editor.oneClickPhaseScript')}</p>}
          {phase === 'voice' && <p className="narration-note" role="status">{t('editor.oneClickPhaseVoice')}</p>}
          {phase === 'animation' && <p className="narration-note" role="status">{t('editor.oneClickPhaseAnimation')}</p>}
          <div className="draft-actions">
            <button
              type="button"
              className="primary"
              disabled={running || !canRun || !(steps.script || steps.voice || steps.animation)}
              onClick={props.onRun}
            >
              {running ? t('editor.oneClickRunning') : t('editor.oneClickSubmit')}
            </button>
            <button type="button" className="button-ghost" disabled={running} onClick={props.onClose}>
              {t('common.cancel')}
            </button>
          </div>
          {error && <p className="form-error" role="alert">{error}</p>}
          {done && (
            <>
              <p className="narration-note" role="status">
                {t('editor.oneClickDone', {
                  script: done.script,
                  applied: done.applied,
                  skipped: done.skipped
                })}
              </p>
              {done.skipped > 0 && done.reasons && (
                <p className="narration-note">
                  {t(done.voiceRan ? 'editor.oneClickSkippedReasonsNoSource' : 'editor.oneClickSkippedReasons', {
                    reasons: done.reasons
                  })}
                </p>
              )}
            </>
          )}
        </section>
      </aside>
    </div>
  );
}
