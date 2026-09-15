import { useRef, useState } from 'react';
import {
  abortUpload,
  completeUpload,
  createUpload,
  sha256Hex,
  uploadToURL,
  type ClientIdentity,
  type CompletedUpload
} from '../api';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import type { Project } from '../types';

// 导入限制（前端校验；后端亦可另行强制）。
const MAX_BYTES = 100 * 1024 * 1024; // 100 MB
const MAX_LABEL = '100 MB';
const PPTX_MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation';

type Phase =
  | 'idle'
  | 'hashing'
  | 'session'
  | 'uploading'
  | 'verifying'
  | 'done'
  | 'doneWithWarnings'
  | 'error'
  | 'canceled';

type Props = {
  identity: ClientIdentity;
  project: Project;
  onClose: () => void;
  onCompleted: (jobId: string) => void;
};

export function ImportDialog({ identity, project, onClose, onCompleted }: Props) {
  const { t } = useI18n();
  const [phase, setPhase] = useState<Phase>('idle');
  const [fileName, setFileName] = useState('');
  const [progress, setProgress] = useState(0);
  const [message, setMessage] = useState('');
  const [result, setResult] = useState<CompletedUpload | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  const busy = phase === 'hashing' || phase === 'session' || phase === 'uploading' || phase === 'verifying';

  const phaseText = (p: Phase): string => {
    switch (p) {
      case 'hashing':
        return t('import.hashing');
      case 'session':
        return t('import.session');
      case 'verifying':
        return t('import.verifying');
      default:
        return t('import.idle');
    }
  };

  const handleFile = async (file: File | null | undefined) => {
    if (!file || busy) return;
    // 格式校验：仅允许 .pptx。
    const isPptx = file.name.toLowerCase().endsWith('.pptx') || file.type === PPTX_MIME;
    if (!isPptx) {
      setFileName(file.name);
      setPhase('error');
      setMessage(t('import.formatLimit'));
      return;
    }
    // 大小校验。
    if (file.size > MAX_BYTES) {
      setFileName(file.name);
      setPhase('error');
      setMessage(t('import.sizeLimit', { max: MAX_LABEL }));
      return;
    }
    setFileName(file.name);
    setProgress(0);
    setMessage('');
    setResult(null);
    const controller = new AbortController();
    abortRef.current = controller;
    let uploadId = '';
    try {
      setPhase('hashing');
      const hash = await sha256Hex(file);
      setPhase('session');
      const session = await createUpload(identity, project.id, file.name, file.size);
      uploadId = session.uploadId;
      const url = session.signedUploadUrls[0];
      if (!url) throw new Error(t('import.noUrl'));
      setPhase('uploading');
      await uploadToURL(url, file, {
        signal: controller.signal,
        onProgress: (loaded, total) => {
          setProgress(total > 0 ? Math.round((loaded / total) * 100) : 0);
        }
      });
      setProgress(100);
      setPhase('verifying');
      const done = await completeUpload(identity, uploadId, hash, file.size);
      setResult(done);
      setPhase(done.warnings && done.warnings.length ? 'doneWithWarnings' : 'done');
      onCompleted(done.jobId);
    } catch (error) {
      const aborted = controller.signal.aborted || (error instanceof DOMException && error.name === 'AbortError');
      if (aborted) {
        setPhase('canceled');
        setMessage(t('import.canceled'));
      } else {
        setPhase('error');
        setMessage(t('import.failed', { error: error instanceof Error ? error.message : String(error) }));
      }
      if (uploadId) {
        try {
          await abortUpload(identity, uploadId);
        } catch {
          // 中止失败不影响主提示。
        }
      }
    } finally {
      abortRef.current = null;
    }
  };

  const handleCancel = () => {
    abortRef.current?.abort();
  };

  const handleViewTask = () => {
    onClose();
    navigate('/jobs');
  };

  const isDone = phase === 'done' || phase === 'doneWithWarnings';

  return (
    <div className="modal-backdrop" role="dialog" aria-label={t('import.aria', { title: project.title })}>
      <section className="modal-card import-modal">
        <header>
          <div>
            <span className="eyebrow">{t('import.eyebrow')}</span>
            <h2>{project.title}</h2>
            <small>{t('import.cloudNote')}</small>
          </div>
          <button type="button" onClick={onClose}>
            {t('common.close')}
          </button>
        </header>

        <label
          className={`upload-entry${dragOver ? ' drag-over' : ''}`}
          onDragOver={(e) => {
            e.preventDefault();
            setDragOver(true);
          }}
          onDragLeave={() => setDragOver(false)}
          onDrop={(e) => {
            e.preventDefault();
            setDragOver(false);
            void handleFile(e.dataTransfer.files?.[0]);
          }}
        >
          <span>{busy ? t('import.busy') : t('import.dragHere')}</span>
          <small className="upload-limit">{t('import.limitHint', { max: MAX_LABEL })}</small>
          <input
            type="file"
            disabled={busy}
            accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation"
            onChange={(e) => {
              void handleFile(e.currentTarget.files?.[0]);
              e.currentTarget.value = '';
            }}
          />
        </label>

        {fileName && <p className="upload-note">{t('import.chosen', { name: fileName })}</p>}

        {phase === 'uploading' && (
          <div
            className="progress"
            role="progressbar"
            aria-valuenow={progress}
            aria-valuemin={0}
            aria-valuemax={100}
          >
            <div className="progress-bar" style={{ width: `${progress}%` }} />
            <span className="progress-label">{t('import.progress', { pct: progress })}</span>
          </div>
        )}

        {isDone ? (
          <div className="report">
            <p className="report-title">{t('import.success')}</p>
            <p className="muted">{t('import.done', { rev: result?.sourceRevisionId ?? '', job: result?.jobId ?? '' })}</p>
            {phase === 'doneWithWarnings' && result?.warnings && result.warnings.length > 0 && (
              <div className="report-list">
                <span className="muted">{t('import.report')}</span>
                <ul>
                  {result.warnings.map((w, i) => (
                    <li key={i}>{w}</li>
                  ))}
                </ul>
              </div>
            )}
            <div className="draft-actions">
              <button type="button" className="primary" onClick={handleViewTask}>
                {t('import.viewTask')}
              </button>
              <button type="button" onClick={onClose}>
                {t('common.close')}
              </button>
            </div>
          </div>
        ) : phase === 'error' || phase === 'canceled' ? (
          <p className={`api-status ${phase === 'error' ? 'error' : ''}`}>
            {message || t('import.failed', { error: '' })}
          </p>
        ) : busy ? (
          <p className="api-status">{message || phaseText(phase)}</p>
        ) : null}

        {phase === 'uploading' && (
          <div className="draft-actions">
            <button type="button" className="button-ghost" onClick={handleCancel}>
              {t('import.cancel')}
            </button>
          </div>
        )}
      </section>
    </div>
  );
}
