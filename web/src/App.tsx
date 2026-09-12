import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ConnectError,
  createGeneration,
  generateDraft,
  getNarration,
  getPlaybackManifest,
  getProjectSlides,
  getScript,
  type ClientIdentity
} from './api';
import { demoManifest, demoScripts, demoTimeline } from './mockData';
import { Player } from './Player';
import { ProjectPanel } from './ProjectPanel';
import { ScriptEditor } from './ScriptEditor';
import type { PlaybackManifest, ScriptRevision, SlideSummary } from './types';
import { usecToClock } from './playerClock';

const devIdentity: ClientIdentity = {
  tenantId: '00000000-0000-0000-0000-000000000000',
  userId: 'dev-user'
};

const demoProjectId = demoManifest.projectId;

type SlidesState =
  | { mode: 'demo'; slides: SlideSummary[] }
  | { mode: 'loading' }
  | { mode: 'empty' }
  | { mode: 'real'; slides: SlideSummary[]; revisionNo: number };

type DraftStatus = { phase: 'idle' | 'generating' | 'ready' | 'error'; message: string };

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

export function App() {
  const [scripts, setScripts] = useState<ScriptRevision[]>(demoScripts);
  const [activeSlideID, setActiveSlideID] = useState(demoTimeline.slides[0].slideId);
  const [activeProjectId, setActiveProjectId] = useState(demoProjectId);
  const [slidesState, setSlidesState] = useState<SlidesState>({ mode: 'demo', slides: [] });
  const [realScripts, setRealScripts] = useState<Record<string, ScriptRevision>>({});
  const [draftStatus, setDraftStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [narrationStatus, setNarrationStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [realManifest, setRealManifest] = useState<PlaybackManifest | null>(null);
  const activeScript = scripts.find((script) => script.slideId === activeSlideID) ?? scripts[0];
  const approvedCount = scripts.filter((script) => script.status !== 'draft').length;
  const manifestExpiry = useMemo(
    () => new Date(demoManifest.expiresAtUnix * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }),
    []
  );

  // 上传完成回调：重新拉取解析页面。
  const onProjectUploaded = useCallback(() => {
    setSlidesState((current) => ({ ...current, mode: 'loading' }));
  }, []);

  // 真实项目：拉取该项目的解析页面列表（无解析结果时显示占位）。
  useEffect(() => {
    if (activeProjectId === demoProjectId) {
      setSlidesState({ mode: 'demo', slides: [] });
      setRealScripts({});
      setDraftStatus({ phase: 'idle', message: '' });
      setNarrationStatus({ phase: 'idle', message: '' });
      setRealManifest(null);
      return;
    }
    let cancelled = false;
    setSlidesState({ mode: 'loading' });
    setRealScripts({});
    setDraftStatus({ phase: 'idle', message: '' });
    setNarrationStatus({ phase: 'idle', message: '' });
    setRealManifest(null);
    getProjectSlides(devIdentity, activeProjectId)
      .then((res) => {
        if (cancelled) return;
        setSlidesState(
          res.slides.length === 0
            ? { mode: 'empty' }
            : { mode: 'real', slides: res.slides, revisionNo: res.revisionNo }
        );
      })
      .catch(() => {
        if (cancelled) return;
        setSlidesState({ mode: 'empty' });
      });
    return () => {
      cancelled = true;
    };
  }, [activeProjectId, onProjectUploaded]);

  // 真实解析页面就绪后，拉取已有讲稿（可能尚未生成）。
  useEffect(() => {
    if (slidesState.mode !== 'real') return;
    let cancelled = false;
    let found: Record<string, ScriptRevision> = {};
    const fetchExisting = async () => {
      for (const slide of slidesState.slides) {
        if (cancelled) return;
        try {
          const rev = await getScript(devIdentity, activeProjectId, slide.slideId);
          if (cancelled) return;
          found[slide.slideId] = rev;
        } catch {
          // 未生成讲稿时跳过。
        }
      }
      if (!cancelled) setRealScripts((current) => ({ ...current, ...found }));
    };
    void fetchExisting();
    return () => {
      cancelled = true;
    };
  }, [slidesState, activeProjectId]);

  const updateScript = (next: ScriptRevision) => {
    setScripts((current) => current.map((script) => (script.slideId === next.slideId ? next : script)));
  };

  const updateRealScript = (next: ScriptRevision) => {
    setRealScripts((current) => ({ ...current, [next.slideId]: next }));
  };

  // 生成原文讲稿：入队 script_draft 并轮询全部页面讲稿。
  const generateAll = async () => {
    if (slidesState.mode !== 'real' || draftStatus.phase === 'generating') return;
    setDraftStatus({ phase: 'generating', message: '讲稿生成任务已入队…' });
    try {
      await generateDraft(devIdentity, activeProjectId, slidesState.slides.map((slide) => slide.slideId));
      const deadline = Date.now() + 120_000;
      let found: Record<string, ScriptRevision> = {};
      while (Date.now() < deadline) {
        const missing = slidesState.slides.filter((slide) => !found[slide.slideId]);
        if (missing.length === 0) break;
        for (const slide of missing) {
          try {
            const rev = await getScript(devIdentity, activeProjectId, slide.slideId);
            found[slide.slideId] = rev;
          } catch {
            // 尚未生成，继续等待。
          }
        }
        setRealScripts(found);
        if (slidesState.slides.every((slide) => found[slide.slideId])) break;
        await sleep(1500);
      }
      const ready = slidesState.slides.every((slide) => found[slide.slideId]);
      setDraftStatus(
        ready
          ? { phase: 'ready', message: `讲稿生成完成：${slidesState.slides.length} 页` }
          : { phase: 'error', message: '讲稿仍在后台生成，可稍后刷新查看。' }
      );
    } catch (error) {
      setDraftStatus({
        phase: 'error',
        message: error instanceof Error ? error.message : '讲稿生成失败'
      });
    }
  };

  const isRealProject = activeProjectId !== demoProjectId;
  const isRealSlides = slidesState.mode === 'real';
  const activeRealScript = isRealSlides ? realScripts[activeSlideID] : undefined;

  // 生成配音：CreateGeneration → 轮询 GetNarration → 取真实播放 manifest。
  const generateNarration = async () => {
    if (!isRealSlides || slidesState.mode !== 'real' || narrationStatus.phase === 'generating') return;
    if (Object.keys(realScripts).length < slidesState.slides.length) {
      setNarrationStatus({ phase: 'error', message: '请先生成全部页面的讲稿再配音。' });
      return;
    }
    setNarrationStatus({ phase: 'generating', message: '配音任务已入队…' });
    try {
      const slideIds = slidesState.slides.map((slide) => slide.slideId);
      const idempotencyKey = `narration-${activeProjectId}-${Date.now()}`;
      await createGeneration(devIdentity, activeProjectId, slideIds, 'fake-voice-1', idempotencyKey);
      const deadline = Date.now() + 120_000;
      let status;
      while (Date.now() < deadline) {
        await sleep(1500);
        status = await getNarration(devIdentity, activeProjectId);
        if (status.ready) break;
      }
      if (!status || !status.ready) {
        setNarrationStatus({ phase: 'error', message: '配音仍在后台生成，可稍后点击重新获取。' });
        return;
      }
      const manifest = await getPlaybackManifest({
        identity: devIdentity,
        projectId: activeProjectId,
        timelineKey: status.timelineKey,
        pagePngKeys: status.pagePngKeys,
        ttlSeconds: 900
      });
      setRealManifest(manifest);
      setNarrationStatus({ phase: 'ready', message: `配音已就绪：${status.pagePngKeys.length > 0 ? '含页面图' : '页面渲染未就绪（音频+字幕可播）'}` });
    } catch (error) {
      setNarrationStatus({
        phase: 'error',
        message: error instanceof Error ? error.message : '配音生成失败'
      });
    }
  };

  const slideRail = () => {
    if (isRealSlides && slidesState.mode === 'real') {
      return slidesState.slides.map((slide, index) => (
        <button
          key={slide.slideId}
          type="button"
          className={slide.slideId === activeSlideID ? 'selected' : ''}
          onClick={() => setActiveSlideID(slide.slideId)}
        >
          <span>{String(index + 1).padStart(2, '0')}</span>
          <strong>{slide.title || slide.slideId}</strong>
          <em>{slide.preview || (slide.hasNotes ? '有备注' : '')}</em>
        </button>
      ));
    }
    return demoTimeline.slides.map((slide, index) => {
      const script = scripts.find((item) => item.slideId === slide.slideId);
      return (
        <button
          key={slide.slideId}
          type="button"
          className={slide.slideId === activeSlideID ? 'selected' : ''}
          onClick={() => setActiveSlideID(slide.slideId)}
        >
          <span>{String(index + 1).padStart(2, '0')}</span>
          <strong>{slide.slideId}</strong>
          <em>{script && script.status !== 'draft' ? '已审' : '待审'}</em>
        </button>
      );
    });
  };

  return (
    <main className="app-shell">
      <header className="topbar">
        <div>
          <span className="eyebrow">PPTS Workspace</span>
          <h1>{isRealProject ? activeProjectId : '产品发布会讲解工程'}</h1>
        </div>
        <div className="status-pills" aria-label="工程状态">
          {isRealSlides && slidesState.mode === 'real' ? (
            <>
              <span>revision {slidesState.revisionNo} · {slidesState.slides.length} 页</span>
              <span>{Object.keys(realScripts).length}/{slidesState.slides.length} 页讲稿</span>
            </>
          ) : isRealProject ? (
            <span>等待解析页面</span>
          ) : (
            <>
              <span>{approvedCount}/{scripts.length} 已审核</span>
              <span>{usecToClock(demoTimeline.durationUs)} 总时长</span>
              <span>签名资源 {manifestExpiry} 过期</span>
            </>
          )}
        </div>
      </header>

      <section className="workspace">
        <aside className="slide-rail" aria-label="页面列表">
          <ProjectPanel
            identity={devIdentity}
            activeProjectId={activeProjectId}
            onProjectSelect={setActiveProjectId}
            onProjectUploaded={onProjectUploaded}
          />
          {isRealProject && !isRealSlides && (
            <p className="empty-state">
              {slidesState.mode === 'loading' ? '解析结果加载中…' : '尚未解析页面：请选择 PPTX 上传并等待解析完成。'}
            </p>
          )}
          <div className="rail-title">页面</div>
          {slideRail()}
        </aside>

        {isRealProject ? (
          isRealSlides && activeRealScript ? (
            <ScriptEditor script={activeRealScript} onChange={updateRealScript} />
          ) : (
            <section className="editor-card">
              <header>
                <h2>讲稿</h2>
                <span className="eyebrow">真实项目</span>
              </header>
              {isRealSlides ? (
                <>
                  <p className="empty-state">
                    {draftStatus.message ||
                      '解析完成。点击下方按钮从原文生成逐页讲稿，生成后可直接在此编辑。'}
                  </p>
                  <div className="draft-actions">
                    <button
                      type="button"
                      disabled={draftStatus.phase === 'generating'}
                      onClick={() => void generateAll()}
                    >
                      {draftStatus.phase === 'generating' ? '生成中…' : '生成原文讲稿'}
                    </button>
                  </div>
                </>
              ) : (
                <p className="empty-state">尚未上传解析，无法生成讲稿。</p>
              )}
            </section>
          )
        ) : (
          <ScriptEditor script={activeScript} onChange={updateScript} />
        )}

        <section className="preview-column">
          {isRealProject ? (
            realManifest ? (
              <Player manifest={realManifest} onSlideChange={setActiveSlideID} />
            ) : (
              <section className="player-card">
                <span className="eyebrow">播放器预览</span>
                <p className="empty-state">{narrationStatus.message || '生成配音后即可在此试听音频与字幕。'}</p>
                <div className="draft-actions">
                  <button
                    type="button"
                    disabled={narrationStatus.phase === 'generating'}
                    onClick={() => void generateNarration()}
                  >
                    {narrationStatus.phase === 'generating'
                      ? '配音生成中…'
                      : realManifest
                        ? '重新获取配音'
                        : '生成配音'}
                  </button>
                </div>
              </section>
            )
          ) : (
            <Player manifest={demoManifest} onSlideChange={setActiveSlideID} />
          )}
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