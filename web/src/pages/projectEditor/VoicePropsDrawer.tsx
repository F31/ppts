import type { RefObject } from 'react';
import type { VoiceModel } from '../../api';
import type { ProbeId, Translate } from './model';

// 语速档位：与后端 narration 允许的区间一致，改动要同步服务端校验。
const VOICE_RATE_OPTIONS = [75, 90, 100, 110, 125, 150];

/** 试听态：只描述"界面要显示什么"，生命周期仍由 useVoicePreview 负责。 */
export type VoicePreviewState = {
  loading: boolean;
  url: string;
  error: string;
  /** 实际合成所用模型（可能被网关改写），用于"你听到的是哪个模型"。 */
  model: string;
  durationMs: number;
};

export type VoicePropsDrawerProps = {
  dialogRef: RefObject<HTMLElement | null>;
  t: Translate;
  models: VoiceModel[];
  /** 草稿值：改完点「配音」才保存，试听也用这一档。 */
  draftModel: string;
  draftVoice: string;
  draftRate: number;
  genMode: 'incremental' | 'full';
  includeHiddenPages: boolean;
  /** 隐藏页总数与"隐藏页里已有讲稿的页数"（决定提示文案）。 */
  hiddenPageCount: number;
  hiddenScriptCount: number;
  scriptReadyCount: number;
  staleCount: number;
  staleLoading: boolean;
  preview: VoicePreviewState;
  probeErrors: Partial<Record<ProbeId, string>>;
  saveError: string;
  saving: boolean;
  canEdit: boolean;
  onClose: () => void;
  onSelectModel: (name: string) => void;
  onVoiceChange: (voice: string) => void;
  onRateChange: (ratePercent: number) => void;
  onGenModeChange: (mode: 'incremental' | 'full') => void;
  onToggleHiddenPages: (checked: boolean) => void;
  onPreview: () => void;
  onRetryStale: () => void;
  onRetryProbes: () => void;
  onSubmit: () => void;
};

/** 音色显示名：网关返回的是 `provider:voice` 形式，用户只需要后段。 */
export function voiceDisplayName(voice: string): string {
  const raw = voice.trim();
  const suffix = raw.includes(':') ? raw.split(':').pop() || raw : raw;
  return suffix || raw;
}

/**
 * 语音属性抽屉：纯展示组件。
 *
 * 草稿值与"保存语音配置"在 useVoiceDraft，试听在 useVoicePreview，
 * 配音流水线留在编辑器——本组件只负责把这几份状态画出来并发事件。
 */
export function VoicePropsDrawer(props: VoicePropsDrawerProps) {
  const { dialogRef, t, models, draftModel, draftVoice, draftRate, preview, probeErrors } = props;
  const voices = models.find((model) => model.name === draftModel)?.voices ?? [];

  return (
    <div className="drawer-backdrop" onClick={props.onClose}>
      <aside
        className="properties-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={t('editor.properties')}
        onClick={(event) => event.stopPropagation()}
        ref={dialogRef}
      >
        <header>
          <div>
            <span className="eyebrow">{t('editor.properties')}</span>
            <h2>{t('editor.properties')}</h2>
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
          <label className="field-label">
            {t('editor.voiceModel')}
            <select value={draftModel} onChange={(e) => props.onSelectModel(e.currentTarget.value)} disabled={models.length === 0}>
              {models.length === 0 && (
                // 读取失败时不能说"无可用语音模型"——那是把故障说成"平台没这个能力"。
                <option value="">{probeErrors.voiceModels ? t('err.unavailable') : t('editor.voiceModelEmpty')}</option>
              )}
              {models.map((model) => (
                <option key={model.name} value={model.name}>
                  {model.model || model.name}
                  {model.isDefault ? ` · ${t('editor.voiceModelDefault')}` : ''}
                </option>
              ))}
            </select>
          </label>

          <label className="field-label">
            {t('editor.voice')}
            <select value={draftVoice} onChange={(e) => props.onVoiceChange(e.currentTarget.value)} disabled={voices.length === 0}>
              {voices.map((voice) => (
                <option key={voice} value={voice}>
                  {voiceDisplayName(voice)}
                </option>
              ))}
            </select>
          </label>

          {/* 先试听再确认：不必等整篇生成，也不必先保存草稿。
              用的是草稿音色/语速，因此"听到的"就是点击「配音」后会用的参数。 */}
          <div className="voice-preview">
            <button
              type="button"
              className="button-ghost"
              disabled={!draftVoice || preview.loading || !props.canEdit}
              onClick={props.onPreview}
            >
              {preview.loading ? t('editor.voicePreviewLoading') : t('editor.voicePreview')}
            </button>
            {preview.url && <audio controls autoPlay src={preview.url} aria-label={t('editor.voicePreview')} />}
            {preview.model && <p className="narration-note">{t('editor.voicePreviewModel', { model: preview.model })}</p>}
            {preview.durationMs > 0 && (
              <p className="narration-note">
                {t('editor.voicePreviewDuration', { seconds: (preview.durationMs / 1000).toFixed(1) })}
              </p>
            )}
            {preview.error && (
              <p className="form-error" role="alert">
                {preview.error}
              </p>
            )}
          </div>

          <label className="field-label">
            {t('editor.rate', { percent: draftRate })}
            <select value={draftRate} onChange={(e) => props.onRateChange(Number(e.currentTarget.value))}>
              {VOICE_RATE_OPTIONS.map((rate) => (
                <option key={rate} value={rate}>
                  {rate === 100 ? t('editor.rateStandard') : `${rate}%`}
                </option>
              ))}
            </select>
          </label>

          <label className="field-label">
            {t('editor.voiceGenerateMode')}
            <select
              value={props.genMode}
              onChange={(e) => props.onGenModeChange(e.currentTarget.value as 'incremental' | 'full')}
            >
              <option value="incremental">{t('editor.voiceModeIncremental')}</option>
              <option value="full">{t('editor.voiceModeFull')}</option>
            </select>
          </label>

          {/* 隐藏页默认不参与成片：放映与 PDF 导出都会跳过它。默认不配音，
              要纳入得显式勾选——否则用户会在成品里发现"多了一段没人看的内容"。 */}
          <label className="field-check">
            <input
              type="checkbox"
              checked={props.includeHiddenPages}
              onChange={(e) => props.onToggleHiddenPages(e.currentTarget.checked)}
            />
            {t('editor.includeHiddenPages', { count: props.hiddenPageCount })}
          </label>

          {probeErrors.voiceStale ? (
            // 原实现失败即置空数组，这里会显示"语音全部最新"——与"确实无需更新"完全同貌（A26）。
            <div className="load-failure" role="alert">
              <p className="form-error">{probeErrors.voiceStale}</p>
              <button type="button" onClick={props.onRetryStale}>
                {t('common.retry')}
              </button>
            </div>
          ) : (
            <p className="narration-note">
              {props.staleLoading
                ? t('editor.voiceStaleLoading')
                : props.genMode === 'full'
                  ? t('editor.voiceFullHint', { total: props.scriptReadyCount })
                  : t('editor.voiceStaleHint', { stale: props.staleCount, total: props.scriptReadyCount })}
            </p>
          )}
          {!props.includeHiddenPages && props.hiddenScriptCount > 0 && (
            <p className="narration-note">{t('editor.hiddenPagesSkipped', { count: props.hiddenScriptCount })}</p>
          )}

          {(probeErrors.voiceModels || probeErrors.voiceSettings) && (
            // 已存音色读取失败时下面会退回缺省（语速 100% / 开发音色），
            // 不说明的话用户会以为"我保存的音色被清空了"。
            <div className="load-failure load-failure-stack" role="alert">
              {probeErrors.voiceModels && <p className="form-error">{probeErrors.voiceModels}</p>}
              {probeErrors.voiceSettings && <p className="form-error">{probeErrors.voiceSettings}</p>}
              <button type="button" onClick={props.onRetryProbes}>
                {t('common.retry')}
              </button>
            </div>
          )}
          {!probeErrors.voiceModels && models.length === 0 && <p className="narration-note">{t('editor.voiceModelEmpty')}</p>}
          {models.length > 0 && <p className="narration-note">{t('editor.voiceModelNote')}</p>}
          {props.saveError && (
            <p className="form-error" role="alert">
              {props.saveError}
            </p>
          )}

          <div className="draft-actions">
            <button type="button" className="primary" disabled={props.saving || !props.canEdit} onClick={props.onSubmit}>
              {props.saving ? t('editor.saving') : t('editor.voiceSubmit')}
            </button>
            <button type="button" className="button-ghost" onClick={props.onClose}>
              {t('common.cancel')}
            </button>
          </div>
          {!props.canEdit && <p className="perm-hint">{t('perm.needEditorExport')}</p>}
        </section>
      </aside>
    </div>
  );
}
