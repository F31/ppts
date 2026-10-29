import { useState } from 'react';
import { useI18n } from '../i18n';
import type { ArtifactFormat, PlaybackManifest } from '../types';
import type { AnimationExportOptions } from '../api';
import { animationReasonText } from '../animationText';
import { useDialogA11y } from '../a11y';

export type ExportOptions = { burnSubtitles: boolean; includeNotes: boolean; animation: boolean };

// ExportFormat 比 ArtifactFormat 多一个 html_bundle：它不走 Connect（proto enum 里没有该值），
// 而是走原生 REST 端点。两者对用户是同一层"导出格式"，故在这里统一。
export type ExportFormat = ArtifactFormat | 'html_bundle';

type ExportChoice = {
  format: ExportFormat;
  labelKey: string;
  enabled: boolean;
  gateKey?: string;
  noteKey?: string;
};

// 导出类型与门禁：音频包后端尚未提供（显式标注门禁，不静默失败）；
// 配音 PPTX 已接通：在源 PPTX 副本上嵌入逐页音频并设置自动切页；
// HTML 成品包（弹窗最上、默认选中）：解压双击 index.html 即可播放，无需后端与网络。
const CHOICES: ExportChoice[] = [
  { format: 'html_bundle', labelKey: 'editor.fmtHtmlBundle', enabled: true, noteKey: 'editor.fmtHtmlBundleNote' },
  { format: 'ARTIFACT_FORMAT_WEB_PROJECT', labelKey: 'editor.fmtWebProject', enabled: true },
  { format: 'ARTIFACT_FORMAT_MP4', labelKey: 'editor.fmtMp4', enabled: true },
  { format: 'ARTIFACT_FORMAT_SUBTITLE_SRT', labelKey: 'editor.fmtSrt', enabled: true },
  { format: 'ARTIFACT_FORMAT_SUBTITLE_VTT', labelKey: 'editor.fmtVtt', enabled: true },
  { format: 'ARTIFACT_FORMAT_AUDIO_PACK', labelKey: 'editor.fmtAudioPack', enabled: false, gateKey: 'editor.gateAudioPack' },
  { format: 'ARTIFACT_FORMAT_AUDIO_PPTX', labelKey: 'editor.fmtAudioPptx', enabled: true }
];

export function ExportDialog({
  manifest,
  busy,
  error,
  dropped,
  animation,
  animationLoading,
  onClose,
  onSubmit
}: {
  manifest: PlaybackManifest;
  busy: boolean;
  error: string;
  /**
   * 导出已受理但动效被后端摘掉时的说明（含任务号）。
   * 非空时对话框保持打开：这是用户点完「导出」才发现的事实，
   * 关掉再提示就变成一条几秒后消失的消息，用户会以为拿到的是动效视频。
   */
  dropped?: string;
  /** 导出选项探测结果（GET .../exports/animation-options）。null = 还没拿到。 */
  animation?: AnimationExportOptions | null;
  animationLoading?: boolean;
  onClose: () => void;
  onSubmit: (format: ExportFormat, options: ExportOptions) => void;
}) {
  const { t } = useI18n();
  const dialogRef = useDialogA11y<HTMLDivElement>(onClose);
  const [format, setFormat] = useState<ExportFormat>('html_bundle');
  const [burnSubtitles, setBurnSubtitles] = useState(true);
  const [includeNotes, setIncludeNotes] = useState(true);
  const [wantAnimation, setWantAnimation] = useState(false);

  const pagePngCount = manifest.resources.filter((r) => r.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG').length;
  // 页图是可选增强：缺图不再拦截导出（后端缺图页降级为沿用上一页/占位），仅在完全无页图时给出提示性说明。
  const mp4NoImages = format === 'ARTIFACT_FORMAT_MP4' && pagePngCount === 0;
  const snapshotTail = manifest.timelineKey.split('/').slice(-1)[0] || manifest.timelineKey;

  // 动画选项：能不能勾由「部署是否开启 + 渲染服务此刻是否可达 + 至少一页有规格」决定。
  // 三项任一不满足都要说明原因——一个灰掉的复选框不给原因，等于让用户怀疑自己配错了。
  const animationPages = animation?.pages ?? [];
  const animatedPages = animationPages.filter((page) => page.revision > 0).length;
  const canAnimate = Boolean(animation?.enabled) && animatedPages > 0;
  const animationNote = animationLoading
    ? t('editor.exportAnimationChecking')
    : !animation
      ? t('editor.exportAnimationUnavailable')
      : !animation.enabled
        ? animationReasonText(animation.reason, t)
        : animatedPages === 0
          ? t('editor.exportAnimationNone')
          : t('editor.exportAnimationPages', { animated: animatedPages, total: animationPages.length });

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

        {format === 'html_bundle' && <p className="form-hint">{t('editor.fmtHtmlBundleNote')}</p>}

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
            {mp4NoImages && <p className="form-hint" role="note">{t('editor.exportMp4NoPages')}</p>}
            <label className={`export-checkbox${canAnimate ? '' : ' disabled'}`}>
              <input
                type="checkbox"
                checked={wantAnimation && canAnimate}
                disabled={busy || !canAnimate}
                onChange={(e) => setWantAnimation(e.target.checked)}
              />
              {t('editor.exportAnimationVideo')}
            </label>
            {/* 勾不了的时候必须说清原因，否则用户只会觉得"动画坏了"。 */}
            <p className="form-hint" role="note">{animationNote}</p>
            {canAnimate && wantAnimation && (
              <p className="form-hint" role="note">{t('editor.exportAnimationDegradeNote')}</p>
            )}
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

        {error && <p className="form-error" role="alert">{error}</p>}
        {/* 不是错误（任务已入队），但必须显眼：说清这一单实际拿到的是静态版。 */}
        {dropped && <p className="form-warn" role="status">{dropped}</p>}

        <div className="modal-actions">
          <button type="button" className="button-ghost" onClick={onClose} disabled={busy}>
            {t('editor.exportCancel')}
          </button>
          <button
            type="button"
            className="button-primary"
            disabled={busy}
            onClick={() => onSubmit(format, { burnSubtitles, includeNotes, animation: wantAnimation && canAnimate })}
          >
            {busy ? t('editor.exporting') : t('editor.exportSubmit')}
          </button>
        </div>
      </section>
    </div>
  );
}
