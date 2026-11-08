import type { RefObject } from 'react';
import type { ScriptMode } from '../../types';
import type { OneDraftPlan } from './oneClickPlan';
import type { Translate } from './model';
import type { OneDraftForm } from './useOneDraftForm';

export type OneDraftProgress = { done: number; total: number; message: string };

export type OneDraftOutcome = { generated: number; skipped: number; degraded: number; failed: number };

export type OneDraftDrawerProps = {
  dialogRef: RefObject<HTMLElement | null>;
  t: Translate;
  form: OneDraftForm;
  onFormChange: (patch: Partial<OneDraftForm>) => void;
  plan: OneDraftPlan | null;
  progress: OneDraftProgress;
  result: OneDraftOutcome | null;
  /** 任务成功但读不到逐页步骤：必须说"结果未知"，不能默认全部成功。 */
  resultUnavailable: boolean;
  /** 逐页失败/降级原因（只展示前几条，面板是提示不是日志）。 */
  reasons: string[];
  running: boolean;
  /** 讲稿正在生成（单页生成/一键成片）：避免两条流水线同时写讲稿。 */
  busy: boolean;
  canSubmit: boolean;
  onClose: () => void;
  onSubmit: () => void;
};

/**
 * 成稿抽屉：纯展示组件。
 *
 * 表单值来自 useOneDraftForm（与一键成片共用同一份），
 * 提交与进度轮询留在编辑器——它们要写讲稿、刷新页列表，不属于表单职责。
 */
export function OneDraftDrawer(props: OneDraftDrawerProps) {
  const { dialogRef, t, form, plan, progress, result, resultUnavailable, reasons, running, busy, canSubmit } = props;
  const percent = Math.round((progress.done / Math.max(1, progress.total)) * 100);

  return (
    <div className="drawer-backdrop" onClick={props.onClose}>
      <aside
        className="properties-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={t('editor.oneDraft')}
        onClick={(event) => event.stopPropagation()}
        ref={dialogRef}
      >
        <header>
          <div>
            <span className="eyebrow">{t('editor.oneDraftEyebrow')}</span>
            <h2>{t('editor.oneDraft')}</h2>
          </div>
          <button
            type="button"
            className="dialog-close-icon"
            onClick={props.onClose}
            aria-label={t('common.close')}
            title={t('common.close')}
          >
            ×
          </button>
        </header>
        <section className="panel nested">
          <p className="narration-note">{t('editor.oneDraftNote')}</p>
          {/* 8 项配置收进「高级设置」：默认 0 配置即可提交。
              过去它们全部摊开，点「一键成稿」先要读一屏下拉框，与「一键」的名字相反。 */}
          <details className="one-draft-advanced">
            <summary>{t('editor.oneDraftAdvanced')}</summary>
            <label className="field-label">
              {t('editor.oneDraftLanguage')}
              <select
                value={form.language}
                disabled={running}
                onChange={(e) => props.onFormChange({ language: e.currentTarget.value as 'zh-CN' | 'en-US' })}
              >
                <option value="zh-CN">{t('editor.languageChinese')}</option>
                <option value="en-US">{t('editor.languageEnglish')}</option>
              </select>
            </label>
            <label className="field-label">
              {t('editor.oneDraftSource')}
              <select
                value={form.source}
                disabled={running}
                onChange={(e) =>
                  props.onFormChange({ source: e.currentTarget.value as 'notes_first' | 'page_content' | 'notes_only' })
                }
              >
                <option value="notes_first">{t('editor.sourceNotesFirst')}</option>
                <option value="notes_only">{t('editor.sourceNotesOnly')}</option>
                <option value="page_content">{t('editor.sourcePageContent')}</option>
              </select>
            </label>
            <label className="field-label">
              {t('editor.oneDraftMode')}
              <select
                value={form.mode}
                disabled={running}
                onChange={(e) => props.onFormChange({ mode: e.currentTarget.value as ScriptMode })}
              >
                <option value="SCRIPT_MODE_ORIGINAL">{t('editor.mode.original')}</option>
                <option value="SCRIPT_MODE_POLISH">{t('editor.mode.polish')}</option>
                <option value="SCRIPT_MODE_AI_GENERATED">{t('editor.mode.ai')}</option>
              </select>
            </label>
            <label className="field-label">
              {t('editor.oneDraftLength')}
              <select
                value={form.length}
                disabled={running}
                onChange={(e) => props.onFormChange({ length: e.currentTarget.value as 'brief' | 'standard' | 'detailed' })}
              >
                <option value="brief">{t('editor.lengthBrief')}</option>
                <option value="standard">{t('editor.lengthStandard')}</option>
                <option value="detailed">{t('editor.lengthDetailed')}</option>
              </select>
            </label>
            <label className="field-label">
              {t('editor.oneDraftStyle')}
              <select value={form.style} disabled={running} onChange={(e) => props.onFormChange({ style: e.currentTarget.value })}>
                <option value="专业正式">{t('editor.styleFormal')}</option>
                <option value="销售演示">{t('editor.styleSales')}</option>
                <option value="培训讲解">{t('editor.styleTraining')}</option>
                <option value="口语自然">{t('editor.styleCasual')}</option>
              </select>
            </label>
            <label className="field-label">
              {t('editor.oneDraftAudience')}
              <select value={form.audience} disabled={running} onChange={(e) => props.onFormChange({ audience: e.currentTarget.value })}>
                <option value="通用听众">{t('editor.audienceGeneral')}</option>
                <option value="客户/甲方">{t('editor.audienceClient')}</option>
                <option value="内部汇报">{t('editor.audienceInternal')}</option>
                <option value="技术团队">{t('editor.audienceTechnical')}</option>
                <option value="管理层/投资人">{t('editor.audienceExecutive')}</option>
              </select>
            </label>
            <label className="field-label">
              {t('editor.oneDraftOverwrite')}
              <select
                value={form.overwrite}
                disabled={running}
                onChange={(e) => props.onFormChange({ overwrite: e.currentTarget.value as 'fill_empty' | 'overwrite_all' })}
              >
                <option value="fill_empty">{t('editor.overwriteFillEmpty')}</option>
                <option value="overwrite_all">{t('editor.overwriteAll')}</option>
              </select>
            </label>
          </details>
          <p className="narration-note">{t('editor.oneDraftGuardrails')}</p>
          {plan && (
            <p className="narration-note">
              {t('editor.oneDraftPlan', { generate: plan.generate, skip: plan.skippedExisting })}
              {plan.reliable ? '' : ` ${t('editor.oneDraftPlanEstimate')}`}
            </p>
          )}
          {plan && plan.noText > 0 && (
            <p className="narration-note warn">{t('editor.oneDraftPlanNoText', { count: plan.noText })}</p>
          )}
          {form.source === 'page_content' && form.mode === 'SCRIPT_MODE_ORIGINAL' && (
            <p className="narration-note warn">{t('editor.oneDraftPageContentOriginalHint')}</p>
          )}
          {form.overwrite === 'fill_empty' && (plan?.withNotes ?? 0) > 0 && (
            <p className="narration-note warn">{t('editor.oneDraftAutoNotesNote')}</p>
          )}
          <div className="draft-actions">
            <button type="button" className="primary" disabled={!canSubmit || running || busy} onClick={props.onSubmit}>
              {running ? t('editor.scriptRegenerating') : t('editor.oneDraftSubmit')}
            </button>
            <button type="button" className="button-ghost" onClick={props.onClose}>
              {t('common.cancel')}
            </button>
          </div>
          {progress.total > 0 && (
            <div className="one-draft-progress" role="status" aria-live="polite">
              <div className="one-draft-progress-bar" aria-hidden="true">
                <span style={{ width: `${percent}%` }} />
              </div>
              <span>{progress.message}</span>
            </div>
          )}
          {result && (
            <p className="narration-note" role="status">
              {t('editor.oneDraftResult', { generated: result.generated, skipped: result.skipped })}
            </p>
          )}
          {result && result.degraded > 0 && (
            <p className="narration-note warn" role="status">
              {t('editor.oneDraftDegraded', { degraded: result.degraded })}
            </p>
          )}
          {result && result.failed > 0 && (
            <p className="narration-note warn" role="status">
              {t('editor.oneDraftFailed', { failed: result.failed })}
            </p>
          )}
          {result && reasons.length > 0 && (
            <ul className="narration-note warn" role="status">
              {reasons.map((reason, i) => (
                <li key={`${i}-${reason}`}>{reason}</li>
              ))}
            </ul>
          )}
          {!result && resultUnavailable && (
            <p className="narration-note warn" role="status">{t('editor.oneDraftResultUnknown')}</p>
          )}
          {!canSubmit && <p className="perm-hint">{t('perm.needEditorExport')}</p>}
        </section>
      </aside>
    </div>
  );
}
