import { useEffect, useState } from 'react';
import { archiveProject, createProject, listProjects, type ClientIdentity } from './api';
import type { Project } from './types';

type Props = {
  identity: ClientIdentity;
  activeProjectId: string;
  onProjectSelect: (projectId: string) => void;
};

export function ProjectPanel({ identity, activeProjectId, onProjectSelect }: Props) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [title, setTitle] = useState('');
  const [fileName, setFileName] = useState('');
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
        <span>选择 PPTX</span>
        <input
          type="file"
          accept=".pptx,application/vnd.openxmlformats-officedocument.presentationml.presentation"
          onChange={(event) => setFileName(event.currentTarget.files?.[0]?.name ?? '')}
        />
      </label>
      <p className="upload-note">
        {fileName ? `${fileName} 已选择。上传会发送到云端处理；UploadService 直传链路下一步接入。` : '上传前会明确提示文件将上传至云端。'}
      </p>
      <p className="api-status">{status}</p>
    </section>
  );
}
