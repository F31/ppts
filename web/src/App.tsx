import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ConnectError,
  createGeneration,
  estimateNarration,
  generateDraft,
  getNarration,
  getPlaybackManifest,
  getProjectSlides,
  getScript,
  listGateways,
  updateScript as updateScriptApi,
  type ClientIdentity
} from './api';
import { clearAccessToken, completeOIDCCallback, oidcConfigured, startOIDCLogin, storedAccessToken } from './auth';
import { demoManifest, demoScripts, demoTimeline } from './mockData';
import { AuditPanel } from './AuditPanel';
import { GatewaySettings } from './GatewaySettings';
import { Player } from './Player';
import { ProjectPanel } from './ProjectPanel';
import { ScriptEditor } from './ScriptEditor';
import type { PlaybackManifest, ScriptMode, ScriptRevision, ScriptSegment, SlideSummary } from './types';
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

type ConflictState = {
  slideId: string;
  localText: string;
  latest: ScriptRevision;
} | null;

const scriptModeOptions: Array<{ value: ScriptMode; label: string; description: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', label: '原文朗读', description: '保留原文，最快生成' },
  { value: 'SCRIPT_MODE_POLISH', label: '润色讲解', description: '更自然的演示口播' },
  { value: 'SCRIPT_MODE_AI_GENERATED', label: 'AI 生成讲解', description: '补足衔接与解释' }
];

const modeLabel = (mode?: ScriptMode) => scriptModeOptions.find((item) => item.value === mode)?.label ?? '原文朗读';

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

const devNarrationVoiceID = 'fake-voice-1';

export function App() {
  const [identity, setIdentity] = useState<ClientIdentity>({ ...devIdentity, accessToken: storedAccessToken() });
  const [authStatus, setAuthStatus] = useState('');
  const [scripts, setScripts] = useState<ScriptRevision[]>(demoScripts);
  const [activeSlideID, setActiveSlideID] = useState(demoTimeline.slides[0].slideId);
  const [activeProjectId, setActiveProjectId] = useState(demoProjectId);
  const [slidesState, setSlidesState] = useState<SlidesState>({ mode: 'demo', slides: [] });
  const [realScripts, setRealScripts] = useState<Record<string, ScriptRevision>>({});
  const [draftStatus, setDraftStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [draftMode, setDraftMode] = useState<ScriptMode>('SCRIPT_MODE_POLISH');
  const [narrationStatus, setNarrationStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [realManifest, setRealManifest] = useState<PlaybackManifest | null>(null);
  const [conflict, setConflict] = useState<ConflictState>(null);
  const [narrationEstimate, setNarrationEstimate] = useState<number | null>(null);
  const [narrationVoiceId, setNarrationVoiceId] = useState(devNarrationVoiceID);
  const [showGateways, setShowGateways] = useState(false);
  const activeScript = scripts.find((script) => script.slideId === activeSlideID) ?? scripts[0];
  const approvedCount = scripts.filter((script) => script.status !== 'draft').length;
  const manifestExpiry = useMemo(
    () => new Date(demoManifest.expiresAtUnix * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }),
    []
  );

  useEffect(() => {
    let cancelled = false;
    completeOIDCCallback()
      .then((token) => {
        if (cancelled || !token) return;
        setIdentity((current) => ({ ...current, accessToken: token }));
        setAuthStatus('OIDC 登录成功');
      })
      .catch((error) => {
        if (!cancelled) setAuthStatus(error instanceof Error ? error.message : 'OIDC 登录失败');
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const refreshNarrationVoice = useCallback(async () => {
    try {
      const gateways = await listGateways(identity, 'tts');
      const gateway = gateways.find((item) => item.enabled && item.isDefault) ?? gateways.find((item) => item.enabled);
      setNarrationVoiceId(gateway?.voice || devNarrationVoiceID);
    } catch {
      // Non-admin users may not read gateway settings; keep the development fallback.
      setNarrationVoiceId(devNarrationVoiceID);
    }
  }, [identity]);

  useEffect(() => {
    void refreshNarrationVoice();
  }, [refreshNarrationVoice]);

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
    getProjectSlides(identity, activeProjectId)
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
  }, [activeProjectId, onProjectUploaded, identity]);

  // 真实解析页面就绪后，拉取已有讲稿（可能尚未生成）。
  useEffect(() => {
    if (slidesState.mode !== 'real') return;
    let cancelled = false;
    let found: Record<string, ScriptRevision> = {};
    const fetchExisting = async () => {
      for (const slide of slidesState.slides) {
        if (cancelled) return;
        try {
          const rev = await getScript(identity, activeProjectId, slide.slideId);
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
  }, [slidesState, activeProjectId, identity]);

  const updateScript = (next: ScriptRevision) => {
    setScripts((current) => current.map((script) => (script.slideId === next.slideId ? next : script)));
  };

  const updateRealScript = (next: ScriptRevision) => {
    setRealScripts((current) => ({ ...current, [next.slideId]: next }));
  };

  const commitRealScript = useCallback(
    async (segments: ScriptSegment[], expectedRevision: number): Promise<ScriptRevision> => {
      const result = await updateScriptApi(identity, activeProjectId, activeSlideID, expectedRevision, segments);
      if (result.conflict && result.latest) {
        // G2-2 并排对比：保留本地修改，进入冲突对比（不静默覆盖）。
        setConflict({
          slideId: activeSlideID,
          localText: segments.map((segment) => segment.displayText).join('\n\n'),
          latest: result.latest
        });
        throw new Error('讲稿已在别处修改，已进入对比视图。');
      }
      return result.revision;
    },
    [identity, activeProjectId, activeSlideID]
  );

  const acceptLatest = () => {
    if (!conflict) return;
    setRealScripts((current) => ({ ...current, [conflict.latest.slideId]: conflict.latest }));
    setConflict(null);
  };

  const retryWithLatest = async () => {
    if (!conflict) return;
    const segments = conflict.latest.segments.map((segment, index) => ({
      ...segment,
      displayText: conflict.localText.split(/\n{2,}/)[index] ?? conflict.localText,
      spokenText: conflict.localText.split(/\n{2,}/)[index] ?? conflict.localText
    }));
    try {
      const result = await updateScriptApi(identity, activeProjectId, conflict.slideId, conflict.latest.revision, segments);
      if (result.conflict && result.latest) {
        setConflict({ ...conflict, latest: result.latest });
        return;
      }
      setRealScripts((current) => ({ ...current, [conflict.slideId]: result.revision }));
      setConflict(null);
    } catch (error) {
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : '讲稿保存失败' });
    }
  };

  const commitError = (message: string) => {
    setDraftStatus({ phase: 'error', message });
  };

  // 生成讲稿：入队 script_draft 并轮询全部页面讲稿。
  const generateAll = async () => {
    if (slidesState.mode !== 'real' || draftStatus.phase === 'generating') return;
    setDraftStatus({ phase: 'generating', message: `${modeLabel(draftMode)}任务已入队…` });
    try {
      await generateDraft(identity, activeProjectId, slidesState.slides.map((slide) => slide.slideId), draftMode);
      const deadline = Date.now() + 120_000;
      let found: Record<string, ScriptRevision> = {};
      while (Date.now() < deadline) {
        const missing = slidesState.slides.filter((slide) => !found[slide.slideId]);
        if (missing.length === 0) break;
        for (const slide of missing) {
          try {
            const rev = await getScript(identity, activeProjectId, slide.slideId);
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

  // 生成配音：预估 → CreateGeneration → 轮询 GetNarration → 取真实播放 manifest。
  const generateNarration = async () => {
    if (!isRealSlides || slidesState.mode !== 'real' || narrationStatus.phase === 'generating') return;
    if (Object.keys(realScripts).length < slidesState.slides.length) {
      setNarrationStatus({ phase: 'error', message: '请先生成全部页面的讲稿再配音。' });
      return;
    }
    setNarrationStatus({ phase: 'generating', message: '配音任务已入队…' });
    try {
      const slideIds = slidesState.slides.map((slide) => slide.slideId);
      const voiceId = narrationVoiceId || devNarrationVoiceID;
      // G2-5 超预算前预估：生成前展示预计时长，超预算时给出选择而非静默失败。
      try {
        const est = await estimateNarration(identity, activeProjectId, slideIds, voiceId);
        setNarrationEstimate(est.estimatedSeconds);
        setNarrationStatus({ phase: 'generating', message: `预计生成时长 ${Math.round(est.estimatedSeconds / 60)} 分钟，配音任务已入队…` });
      } catch {
        // 预估失败不阻塞生成。
      }
      const idempotencyKey = `narration-${activeProjectId}-${Date.now()}`;
      await createGeneration(identity, activeProjectId, slideIds, voiceId, idempotencyKey);
      const deadline = Date.now() + 120_000;
      let status;
      while (Date.now() < deadline) {
        await sleep(1500);
        status = await getNarration(identity, activeProjectId);
        if (status.ready) break;
      }
      if (!status || !status.ready) {
        setNarrationStatus({ phase: 'error', message: '配音仍在后台生成，可稍后点击重新获取。' });
        return;
      }
      const manifest = await getPlaybackManifest({
        identity,
        projectId: activeProjectId,
        timelineKey: status.timelineKey,
        pagePngKeys: status.pagePngKeys,
        ttlSeconds: 900
      });
      setRealManifest(manifest);
      setNarrationStatus({ phase: 'ready', message: `配音已就绪：${status.pagePngKeys.length > 0 ? '含页面图' : '页面渲染未就绪（音频+字幕可播）'}` });
    } catch (error) {
      let message = error instanceof Error ? error.message : '配音生成失败';
      // G2-5 超预算选择：额度不足时给出明确选项而非静默失败。
      if (error instanceof ConnectError && error.code === 'resource_exhausted') {
        const est = narrationEstimate != null ? Math.round(narrationEstimate / 60) : null;
        message = est
          ? `生成额度不足（预计还需 ${est} 分钟）。可减少页面后重试，或联系管理员调整额度。`
          : '生成额度不足。可减少页面后重试，或联系管理员调整额度。';
      }
      setNarrationStatus({
        phase: 'error',
        message
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
          <button
            type="button"
            className="auth-action"
            onClick={() => setShowGateways(true)}
            title="配置 TTS / LLM 模型网关"
          >
            模型网关
          </button>
          {oidcConfigured() && (
            <button
              type="button"
              className="auth-action"
              onClick={() => {
                if (identity.accessToken) {
                  clearAccessToken();
                  setIdentity({ ...devIdentity });
                  setAuthStatus('已退出 OIDC');
                } else {
                  void startOIDCLogin().catch((error) => setAuthStatus(error instanceof Error ? error.message : 'OIDC 登录失败'));
                }
              }}
            >
              {identity.accessToken ? '退出登录' : 'OIDC 登录'}
            </button>
          )}
          {authStatus && <span>{authStatus}</span>}
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
            identity={identity}
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
            <ScriptEditor
              script={activeRealScript}
              onChange={updateRealScript}
              commit={isRealProject ? commitRealScript : undefined}
              onCommitError={commitError}
            />
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
                      '解析完成。选择讲稿模式后生成逐页讲稿，生成后可直接在此编辑。'}
                  </p>
                  <div className="draft-options" aria-label="讲稿模式">
                    {scriptModeOptions.map((option) => (
                      <label key={option.value} className={draftMode === option.value ? 'selected' : ''}>
                        <input
                          type="radio"
                          name="draft-mode"
                          value={option.value}
                          checked={draftMode === option.value}
                          onChange={() => setDraftMode(option.value)}
                        />
                        <span>{option.label}</span>
                        <small>{option.description}</small>
                      </label>
                    ))}
                  </div>
                  <div className="draft-actions">
                    <button
                      type="button"
                      disabled={draftStatus.phase === 'generating'}
                      onClick={() => void generateAll()}
                    >
                      {draftStatus.phase === 'generating' ? '生成中…' : `生成${modeLabel(draftMode)}`}
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
          {conflict && (
            <section className="compare-panel" aria-label="讲稿冲突对比">
              <header>
                <span className="eyebrow">冲突对比</span>
                <h3>{conflict.slideId}</h3>
              </header>
              <div className="compare-columns">
                <div>
                  <h4>我的修改</h4>
                  <pre>{conflict.localText}</pre>
                </div>
                <div>
                  <h4>服务器最新版（revision {conflict.latest.revision}）</h4>
                  <pre>{conflict.latest.segments.map((segment) => segment.displayText).join('\n\n')}</pre>
                </div>
              </div>
              <div className="draft-actions">
                <button type="button" onClick={() => void retryWithLatest()}>
                  以我的修改重试保存
                </button>
                <button type="button" onClick={acceptLatest}>
                  采用服务器最新版
                </button>
              </div>
            </section>
          )}
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
          <AuditPanel identity={identity} />
        </section>
      </section>
      {showGateways && <GatewaySettings identity={identity} onSaved={refreshNarrationVoice} onClose={() => setShowGateways(false)} />}
    </main>
  );
}
