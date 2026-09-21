import { useState } from 'react';
import { useI18n } from '../i18n';
import type { ArtifactFormat, PlaybackManifest } from '../types';
import { useDialogA11y } from '../a11y';

export type ExportOptions = { burnSubtitles: boolean; includeNotes: boolean };

type ExportChoice = {
  format: ArtifactFormat;
  labelKey: string;
  enabled: boolean;
  gateKey?: string;
};

// 导出类型与门禁：后端 exportFormat 仅支持 Web 工程 / MP4 / SRT / VTT；
// 音频包与配音 PPTX 后端尚未提供，前端显式标注门禁而非静默失败。
const CHOICES: ExportChoice[] = [
  { format: 'ARTIFACT_FORMAT_WEB_PROJECT', labelKey: 'editor.fmtWebProject', enabled: true },
  { format: 'ARTIFACT_FORMAT_MP4', labelKey: 'editor.fmtMp4', enabled: true },
  { format: 'ARTIFACT_FORMAT_SUBTITLE_SRT', labelKey: 'editor.fmtSrt', enabled: true },
  { format: 'ARTIFACT_FORMAT_SUBTITLE_VTT', labelKey: 'editor.fmtVtt', enabled: true },
  { format: 'ARTIFACT_FORMAT_AUDIO_PACK', labelKey: 'editor.fmtAudioPack', enabled: false, gateKey: 'editor.gateAudioPack' },
  { format: 'ARTIFACT_FORMAT_AUDIO_PPTX', labelKey: 'editor.fmtAudioPptx', enabled: false, gateKey: 'editor.gateAudioPptx' }
];

export function ExportDialog({
  manifest,
  busy,
  error,
  onClose,
  onSubmit
}: {
  manifest: PlaybackManifest;
  busy: boolean;
  error: string;
  onClose: () => void;
  onSubmit: (format: ArtifactFormat, options: ExportOptions) => void;
}) {
  const { t } = useI18n();
  const dialogRef = useDialogA11y<HTMLDivElement>(onClose);
  const [format, setFormat] = useState<ArtifactFormat>('ARTIFACT_FORMAT_WEB_PROJECT');
  const [burnSubtitles, setBurnSubtitles] = useState(true);
  const [includeNotes, setIncludeNotes] = useState(true);

  const pagePngCount = manifest.resources.filter((r) => r.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG').length;
  const mp4NoPages = format === 'ARTIFACT_FORMAT_MP4' && pagePngCount === 0;
  const snapshotTail = manifest.timelineKey.split('/').slice(-1)[0] || manifest.timelineKey;

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label={t('editor.exportTitle')} ref={dialogRef}>
      <section className="modal-card export-dialog">
        <header>
          <div>
            <span className="eyebrow">{t('editor.exportEyebrow')}</span>
            <h2>{t('editor.exportTitle')}</h2>
          </div>
          <button type="button" onClick={onClose} disabled={busy}>
            {t('common.close')}
          </button>
        </header>

        <p className="form-hint">{t('editor.exportHint')}</p>

        <fieldset className="export-choices">
          <legend>{t('editor.exportFormat')}</legend>
          {CHOICES.map((choice) => (
            <label key={choice.format} className={`export-choice${choice.enabled ? '' : ' disabled'}`}>
              <input
                type="radio"
                name="export-format"
                value={choice.format}
                checked={format === choice.format}
                disabled={!choice.enabled || busy}
                onChange={() => setFormat(choice.format)}
              />
              <span className="export-choice-label">{t(choice.labelKey)}</span>
              {!choice.enabled && <span className="export-gate-tag">{t('editor.exportGated')}</span>}
              {!choice.enabled && choice.gateKey && <small className="export-gate-note">{t(choice.gateKey)}</small>}
            </label>
          ))}
        </fieldset>

        {format === 'ARTIFACT_FORMAT_MP4' && (
          <div className="export-options">
            <label className="export-checkbox">
              <input
                type="checkbox"
                checked={burnSubtitles}
                disabled={busy}
                onChange={(e) => setBurnSubtitles(e.target.checked)}
              />
              {t('editor.exportBurnSubtitles')}
            </label>
            <p className="form-hint">{t('editor.exportBurnNote')}</p>
            <p className="form-hint">{t('editor.exportResolutionNote')}</p>
            {mp4NoPages && <p className="form-error">{t('editor.exportMp4NoPages')}</p>}
          </div>
        )}

        <label className="export-checkbox">
          <input
            type="checkbox"
            checked={includeNotes}
            disabled={busy}
            onChange={(e) => setIncludeNotes(e.target.checked)}
          />
          {t('editor.exportIncludeNotes')}
        </label>
        {/* A26：该选项目前只改快照标识，不写进产物 —— 明说，避免"点了却看不到效果"的假能力。 */}
        <p className="form-hint">{t('editor.exportIncludeNotesNote')}</p>

        <p className="export-snapshot">{t('editor.exportSnapshotNote', { snapshot: snapshotTail })}</p>

        {error && <p className="form-error">{error}</p>}

        <div className="modal-actions">
          <button type="button" className="button-ghost" onClick={onClose} disabled={busy}>
            {t('editor.exportCancel')}
          </button>
          <button
            type="button"
            className="button-primary"
            disabled={busy || mp4NoPages}
            onClick={() => onSubmit(format, { burnSubtitles, includeNotes })}
          >
            {busy ? t('editor.exporting') : t('editor.exportSubmit')}
          </button>
        </div>
      </section>
    </div>
  );
}
