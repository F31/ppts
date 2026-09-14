import { useState } from 'react';
import {
  abortUpload,
  completeUpload,
  createUpload,
  sha256Hex,
  uploadToURL,
  type ClientIdentity
} from '../api';
import type { Project } from '../types';

type Props = {
  identity: ClientIdentity;
  project: Project;
  onClose: () => void;
  onCompleted: (jobId: string) => void;
};

export function ImportDialog({ identity, project, onClose, onCompleted }: Props) {
  const [fileName, setFileName] = useState('');
  const [status, setStatus] = useState('选择一份 PPTX 文件开始导入（处理位置：云端）。');
  const [busy, setBusy] = useState(false);

  const handleFile = async (file: File | undefined) => {
    if (!file || busy) return;
    setFileName(file.name);
    setBusy(true);
    setStatus('计算文件哈希（SHA-256）…');
    let uploadId = '';
    try {
      const hash = await sha256Hex(file);
      setStatus('申请上传会话…');
      const session = await createUpload(identity, project.id, file.name, file.size);
      uploadId = session.uploadId;
      const url = session.signedUploadUrls[0];
      if (!url) throw new Error('未返回可用的上传链接');
      setStatus('上传文件至云端…');
      await uploadToURL(url, file);
      setStatus('校验哈希并创建解析任务…');
      const done = await completeUpload(identity, uploadId, hash, file.size);
      setStatus(
        `导入完成：源版本 ${done.sourceRevisionId}，解析任务 ${done.jobId}${
          done.warnings.length ? `（提示：${done.warnings.join('; ')}）` : ''
        }`
      );
      onCompleted(done.jobId);
    } catch (error) {
      setStatus(`导入失败：${error instanceof Error ? error.message : String(error)}`);
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
    <div className="modal-backdrop" role="dialog" aria-label={`导入 ${project.title}`}>
      <section className="modal-card import-modal">
        <header>
          <div>
            <span className="eyebrow">导入讲解项目</span>
            <h2>{project.title}</h2>
            <small>处理位置：云端服务处理。上传后自动解析页面；页面陆续可用后即可进入工作台。</small>
          </div>
          <button type="button" onClick={onClose}>关闭</button>
        </header>
        <label className="upload-entry">
          <span>{busy ? '处理中…' : '选择 PPTX 上传'}</span>
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
        {fileName && <p className="upload-note">已选择：{fileName}</p>}
        <p className={`api-status ${status.startsWith('导入失败') ? 'error' : ''}`}>{status}</p>
      </section>
    </div>
  );
}