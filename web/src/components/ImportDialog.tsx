import { useState } from 'react';
import {
  abortUpload,
  completeUpload,
  createUpload,
  sha256Hex,
  uploadToURL,
  type ClientIdentity
} from '../api';
import { useI18n } from '../i18n';
import type { Project } from '../types';

type Props = {
  identity: ClientIdentity;
  project: Project;
  onClose: () => void;
  onCompleted: (jobId: string) => void;
};

export function ImportDialog({ identity, project, onClose, onCompleted }: Props) {
  const { t } = useI18n();
  const [fileName, setFileName] = useState('');
  const [status, setStatus] = useState(() => t('import.idle'));
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  const handleFile = async (file: File | undefined) => {
    if (!file || busy) return;
    setFileName(file.name);
    setBusy(true);
    setFailed(false);
    setStatus(t('import.hashing'));
    let uploadId = '';
    try {
      const hash = await sha256Hex(file);
      setStatus(t('import.session'));
      const session = await createUpload(identity, project.id, file.name, file.size);
      uploadId = session.uploadId;
      const url = session.signedUploadUrls[0];
      if (!url) throw new Error(t('import.noUrl'));
      setStatus(t('import.uploading'));
      await uploadToURL(url, file);
      setStatus(t('import.verifying'));
      const done = await completeUpload(identity, uploadId, hash, file.size);
      const warnings = done.warnings ?? [];
      setStatus(
        warnings.length
          ? t('import.doneHint', { rev: done.sourceRevisionId, job: done.jobId, hints: warnings.join('; ') })
          : t('import.done', { rev: done.sourceRevisionId, job: done.jobId })
      );
      onCompleted(done.jobId);
    } catch (error) {
      setFailed(true);
      setStatus(t('import.failed', { error: error instanceof Error ? error.message : String(error) }));
      if (uploadId) {
        try {
          await abortUpload(identity, uploadId);
        } catch {
          // 中止失败不影响主错误提示。
        }
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-label={t('import.aria', { title: project.title })}>
      <section className="modal-card import-modal">
        <header>
          <div>
            <span className="eyebrow">{t('import.eyebrow')}</span>
            <h2>{project.title}</h2>
            <small>{t('import.cloudNote')}</small>
          </div>
          <button type="button" onClick={onClose}>{t('common.close')}</button>
        </header>
        <label className="upload-entry">
          <span>{busy ? t('import.busy') : t('import.select')}</span>
          <input
            type="file"
            disabled={busy}
            accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation"
            onChange={(event) => {
              void handleFile(event.currentTarget.files?.[0]);
              event.currentTarget.value = '';
            }}
          />
        </label>
        {fileName && <p className="upload-note">{t('import.chosen', { name: fileName })}</p>}
        <p className={`api-status ${failed ? 'error' : ''}`}>{status}</p>
      </section>
    </div>
  );
}
