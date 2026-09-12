import { useEffect, useState } from 'react';
import {
  abortUpload,
  archiveProject,
  completeUpload,
  createProject,
  createUpload,
  listProjects,
  sha256Hex,
  uploadToURL,
  type ClientIdentity
} from './api';
import type { Project } from './types';

type Props = {
  identity: ClientIdentity;
  activeProjectId: string;
  onProjectSelect: (projectId: string) => void;
  onProjectUploaded?: () => void;
};

// 上传目标：仅当选中了真实后端项目（非 demo 播放工程）时允许直传。
function uploadTarget(activeProjectId: string, projects: Project[]): Project | undefined {
  return projects.find((project) => project.id === activeProjectId);
}

export function ProjectPanel({ identity, activeProjectId, onProjectSelect, onProjectUploaded }: Props) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [title, setTitle] = useState('');
  const [fileName, setFileName] = useState('');
  const [uploading, setUploading] = useState(false);
  const [status, setStatus] = useState('连接后端加载项目');

  const refresh = async () => {
    setStatus('加载项目中');
    try {
      const next = await listProjects(identity);
      setProjects(next);
      if (next[0] && !activeProjectId) onProjectSelect(next[0].id);
      setStatus(next.length === 0 ? '暂无项目，可创建一个' : '项目已同步');
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '项目加载失败');
    }
  };

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const create = async () => {
    const trimmed = title.trim();
    if (!trimmed) return;
    setStatus('创建项目中');
    try {
      const project = await createProject(identity, trimmed);
      setProjects((current) => [project, ...current]);
      onProjectSelect(project.id);
      setTitle('');
      setStatus('项目已创建');
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '项目创建失败');
    }
  };

  const archive = async (id: string) => {
    setStatus('归档项目中');
    try {
      await archiveProject(identity, id);
      setProjects((current) => current.filter((project) => project.id !== id));
      setStatus('项目已归档');
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '归档失败');
    }
  };

  const handleFile = async (file: File | undefined) => {
    if (!file || uploading) return;
    setFileName(file.name);
    setUploading(true);
    setStatus('准备直传');
    const target = uploadTarget(activeProjectId, projects);
    if (!target) {
      setStatus('请先在左侧创建/选择一个真实项目，demo 播放工程不支持上传');
      setUploading(false);
      return;
    }
    let uploadId = '';
    try {
      setStatus('计算 SHA-256');
      const hash = await sha256Hex(file);
      setStatus('获取预签名直传链接');
      const session = await createUpload(identity, target.id, file.name, file.size);
      uploadId = session.uploadId;
      const url = session.signedUploadUrls[0];
      if (!url) {
        throw new Error('CreateUpload 未返回可用的预签名直传链接');
      }
      setStatus('上传文件至云端');
      await uploadToURL(url, file);
      setStatus('校验哈希并创建解析任务');
      const done = await completeUpload(identity, uploadId, hash, file.size);
      setStatus(`已入队解析：源版本 ${done.sourceRevisionId} / 任务 ${done.jobId}${done.warnings.length ? `（提示：${done.warnings.join('; ')}）` : ''}`);
      setFileName('');
      await refresh();
      onProjectUploaded?.();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setStatus(`上传失败：${message}`);
      if (uploadId) {
        try {
          await abortUpload(identity, uploadId);
        } catch {
          // 中止失败不影响主错误提示。
        }
      }
    } finally {
      setUploading(false);
    }
  };

  return (
    <section className="project-panel" aria-label="项目和上传">
      <div className="panel-row">
        <span className="eyebrow">Project API</span>
        <button type="button" onClick={() => void refresh()}>刷新</button>
      </div>
      <div className="create-project">
        <input value={title} onChange={(event) => setTitle(event.currentTarget.value)} placeholder="新项目标题" />
        <button type="button" onClick={() => void create()}>创建</button>
      </div>
      <div className="project-list">
        {projects.map((project) => (
          <button key={project.id} type="button" className={project.id === activeProjectId ? 'active' : ''} onClick={() => onProjectSelect(project.id)}>
            <strong>{project.title}</strong>
            <span>rev {project.currentRevision}</span>
            <small onClick={(event) => { event.stopPropagation(); void archive(project.id); }}>归档</small>
          </button>
        ))}
      </div>
      <label className="upload-entry">
        <span>{uploading ? '上传中…' : '选择 PPTX 发起直传'}</span>
        <input
          type="file"
          disabled={uploading}
          accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation"
          onChange={(event) => { void handleFile(event.currentTarget.files?.[0]); event.currentTarget.value = ''; }}
        />
      </label>
      <p className="upload-note">
        {fileName ? `${fileName} 已选择，将上传至云端处理（明示文案）。` : '上传前会明确提示文件将上传至云端。'}
      </p>
      <p className="api-status">{status}</p>
    </section>
  );
}