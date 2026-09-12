import { useMemo, useState } from 'react';
import { demoManifest, demoScripts, demoTimeline } from './mockData';
import { Player } from './Player';
import { ProjectPanel } from './ProjectPanel';
import { ScriptEditor } from './ScriptEditor';
import type { ScriptRevision } from './types';
import { usecToClock } from './playerClock';

const devIdentity = {
  tenantId: '00000000-0000-0000-0000-000000000000',
  userId: 'dev-user'
};

export function App() {
	const [scripts, setScripts] = useState<ScriptRevision[]>(demoScripts);
	const [activeSlideID, setActiveSlideID] = useState(demoTimeline.slides[0].slideId);
	const [activeProjectId, setActiveProjectId] = useState(demoManifest.projectId);
	const activeScript = scripts.find((script) => script.slideId === activeSlideID) ?? scripts[0];
  const approvedCount = scripts.filter((script) => script.status !== 'draft').length;
  const manifestExpiry = useMemo(() => new Date(demoManifest.expiresAtUnix * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }), []);

  const updateScript = (next: ScriptRevision) => {
    setScripts((current) => current.map((script) => (script.slideId === next.slideId ? next : script)));
  };

  return (
    <main className="app-shell">
		<header className="topbar">
			<div>
				<span className="eyebrow">PPTS Workspace</span>
				<h1>{activeProjectId === demoManifest.projectId ? '产品发布会讲解工程' : activeProjectId}</h1>
			</div>
        <div className="status-pills" aria-label="工程状态">
          <span>{approvedCount}/{scripts.length} 已审核</span>
          <span>{usecToClock(demoTimeline.durationUs)} 总时长</span>
          <span>签名资源 {manifestExpiry} 过期</span>
        </div>
      </header>

		<section className="workspace">
			<aside className="slide-rail" aria-label="页面列表">
				<ProjectPanel identity={devIdentity} activeProjectId={activeProjectId} onProjectSelect={setActiveProjectId} />
				<div className="rail-title">页面</div>
          {demoTimeline.slides.map((slide, index) => {
            const script = scripts.find((item) => item.slideId === slide.slideId)!;
            return (
              <button
                key={slide.slideId}
                type="button"
                className={slide.slideId === activeSlideID ? 'selected' : ''}
                onClick={() => setActiveSlideID(slide.slideId)}
              >
                <span>{String(index + 1).padStart(2, '0')}</span>
                <strong>{slide.slideId}</strong>
                <em>{script.status === 'draft' ? '待审' : '已审'}</em>
              </button>
            );
          })}
        </aside>

        <ScriptEditor script={activeScript} onChange={updateScript} />

        <section className="preview-column">
          <Player manifest={demoManifest} onSlideChange={setActiveSlideID} />
          <div className="asset-panel">
            <span className="eyebrow">播放资源</span>
            <ul>
              <li>时间轴：服务器返回，不在前端重算</li>
              <li>页面图：按 timeline 页序绑定</li>
              <li>字幕：SRT/VTT 与播放器 cue 同源</li>
              <li>音频：单一媒体时钟，跳页时只改位置</li>
            </ul>
          </div>
        </section>
      </section>
    </main>
  );
}
