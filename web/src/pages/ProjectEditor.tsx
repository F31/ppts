import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ConnectError,
  createExport,
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
} from '../api';
import { Player } from '../Player';
import { ScriptEditor } from '../ScriptEditor';
import { Link } from '../router';
import type { PlaybackManifest, ScriptMode, ScriptRevision, ScriptSegment, SlideSummary } from '../types';

type SlidesState =
  | { mode: 'loading' }
  | { mode: 'empty' }
  | { mode: 'real'; slides: SlideSummary[]; revisionNo: number };

type DraftStatus = { phase: 'idle' | 'generating' | 'ready' | 'error'; message: string };
type ConflictState = { slideId: string; localText: string; latest: ScriptRevision } | null;

const scriptModeOptions: Array<{ value: ScriptMode; label: string; description: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', label: '原文朗读', description: '保留原文，最快生成' },
  { value: 'SCRIPT_MODE_POLISH', label: '润色讲解', description: '更自然的演示口播' },
  { value: 'SCRIPT_MODE_AI_GENERATED', label: 'AI 生成讲解', description: '补足衔接与解释' }
];

const modeLabel = (mode?: ScriptMode) => scriptModeOptions.find((item) => item.value === mode)?.label ?? '原文朗读';

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

const devNarrationVoiceID = 'fake-voice-1';

export function ProjectEditor({ identity, projectId, draftRequested }: { identity: ClientIdentity; projectId: string; draftRequested?: boolean }) {
  const [slidesState, setSlidesState] = useState<SlidesState>({ mode: 'loading' });
  const [activeSlideID, setActiveSlideID] = useState('');
  const [realScripts, setRealScripts] = useState<Record<string, ScriptRevision>>({});
  const [draftStatus, setDraftStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [draftMode, setDraftMode] = useState<ScriptMode>('SCRIPT_MODE_POLISH');
  const [narrationStatus, setNarrationStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [realManifest, setRealManifest] = useState<PlaybackManifest | null>(null);
  const [conflict, setConflict] = useState<ConflictState>(null);
  const [narrationEstimate, setNarrationEstimate] = useState<number | null>(null);
  const [voiceOptions, setVoiceOptions] = useState<string[]>([]);
  const [voiceId, setVoiceId] = useState('');
  const [ratePercent, setRatePercent] = useState(100);
  const [exporting, setExporting] = useState(false);

  // 页面列表
  useEffect(() => {
    let cancelled = false;
    setSlidesState({ mode: 'loading' });
    setRealScripts({});
    setDraftStatus({ phase: 'idle', message: '' });
    setNarrationStatus({ phase: 'idle', message: '' });
    setRealManifest(null);
    getProjectSlides(identity, projectId)
      .then((res) => {
        if (cancelled) return;
        setSlidesState(res.slides.length === 0 ? { mode: 'empty' } : { mode: 'real', slides: res.slides, revisionNo: res.revisionNo });
        if (res.slides.length > 0) setActiveSlideID(res.slides[0].slideId);
      })
      .catch(() => {
        if (!cancelled) setSlidesState({ mode: 'empty' });
      });
    return () => {
      cancelled = true;
    };
  }, [identity, projectId]);

  // 已有讲稿
  useEffect(() => {
    if (slidesState.mode !== 'real') return;
    let cancelled = false;
    const found: Record<string, ScriptRevision> = {};
    const fetchExisting = async () => {
      for (const slide of slidesState.slides) {
        if (cancelled) return;
        try {
          const rev = await getScript(identity, projectId, slide.slideId);
          if (!cancelled) found[slide.slideId] = rev;
        } catch {
          // 未生成讲稿时跳过。
        }
      }
      if (!cancelled) setRealScripts(found);
    };
    void fetchExisting();
    return () => {
      cancelled = true;
    };
  }, [slidesState, identity, projectId]);

  // 音色列表：从 TTS 模型网关读取 voice（多个值逗号分隔），兜底开发音色。
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const list: string[] = [];
      try {
        const gateways = await listGateways(identity, 'tts');
        for (const gateway of gateways) {
          if (!gateway.enabled || !gateway.voice) continue;
          for (const voice of gateway.voice.split(',')) {
            const trimmed = voice.trim();
            if (trimmed && !list.includes(trimmed)) list.push(trimmed);
          }
        }
      } catch {
        // 非 admin 无法读取网关配置，使用开发兜底。
      }
      if (cancelled) return;
      if (list.length > 0) {
        setVoiceOptions(list);
        setVoiceId((current) => current || list[0]);
      } else {
        setVoiceOptions([devNarrationVoiceID]);
        setVoiceId(devNarrationVoiceID);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [identity]);

  const isReady = slidesState.mode === 'real';
  const activeRealScript = isReady ? realScripts[activeSlideID] : undefined;

  const commitRealScript = useCallback(
    async (segments: ScriptSegment[], expectedRevision: number): Promise<ScriptRevision> => {
      const result = await updateScriptApi(identity, projectId, activeSlideID, expectedRevision, segments);
      if (result.conflict && result.latest) {
        setConflict({
          slideId: activeSlideID,
          localText: segments.map((segment) => segment.displayText).join('\n\n'),
          latest: result.latest
        });
        throw new Error('讲稿已在别处修改，已进入对比视图。');
      }
      return result.revision;
    },
    [identity, projectId, activeSlideID]
  );

  const acceptLatest = () => {
    if (!conflict) return;
    setRealScripts((current) => ({ ...current, [conflict.latest.slideId]: conflict.latest }));
    setConflict(null);
  };

  const retryWithLatest = async () => {
    if (!conflict) return;
    const local = conflict.localText.split(/\n{2,}/);
    const segments = conflict.latest.segments.map((segment, index) => {
      const text = local[index] ?? conflict.localText;
      return { ...segment, displayText: text, spokenText: text };
    });
    try {
      const result = await updateScriptApi(identity, projectId, conflict.slideId, conflict.latest.revision, segments);
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

  const generateAll = async () => {
    if (!isReady || draftStatus.phase === 'generating') return;
    setDraftStatus({ phase: 'generating', message: `${modeLabel(draftMode)}任务已入队…` });
    try {
      await generateDraft(identity, projectId, slidesState.slides.map((slide) => slide.slideId), draftMode);
      const deadline = Date.now() + 120_000;
      const found: Record<string, ScriptRevision> = {};
      while (Date.now() < deadline) {
        const missing = slidesState.slides.filter((slide) => !found[slide.slideId]);
        if (missing.length === 0) break;
        for (const slide of missing) {
          try {
            const rev = await getScript(identity, projectId, slide.slideId);
            found[slide.slideId] = rev;
          } catch {
            // 尚未生成，继续等待。
          }
        }
        setRealScripts({ ...found });
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
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : '讲稿生成失败' });
    }
  };

  const generateNarration = async () => {
    if (!isReady || narrationStatus.phase === 'generating') return;
    if (Object.keys(realScripts).length < slidesState.slides.length) {
      setNarrationStatus({ phase: 'error', message: '请先生成全部页面的讲稿再配音。' });
      return;
    }
    const selectedVoice = voiceId || devNarrationVoiceID;
    setNarrationStatus({ phase: 'generating', message: '配音任务已入队…' });
    try {
      const slideIds = slidesState.slides.map((slide) => slide.slideId);
      try {
        const est = await estimateNarration(identity, projectId, slideIds, selectedVoice);
        setNarrationEstimate(est.estimatedSeconds);
        setNarrationStatus({
          phase: 'generating',
          message: `预计生成时长 ${Math.round(est.estimatedSeconds / 60)} 分钟，配音任务已入队…`
        });
      } catch {
        // 预估失败不阻塞生成。
      }
      const idempotencyKey = `narration-${projectId}-${Date.now()}`;
      await createGeneration(identity, projectId, slideIds, selectedVoice, idempotencyKey);
      const deadline = Date.now() + 120_000;
      let status;
      while (Date.now() < deadline) {
        await sleep(1500);
        status = await getNarration(identity, projectId);
        if (status.ready) break;
      }
      if (!status || !status.ready) {
        setNarrationStatus({ phase: 'error', message: '配音仍在后台生成，可稍后点击重新获取。' });
        return;
      }
      const manifest = await getPlaybackManifest({
        identity,
        projectId,
        timelineKey: status.timelineKey,
        pagePngKeys: status.pagePngKeys,
        ttlSeconds: 900
      });
      setRealManifest(manifest);
      setNarrationStatus({
        phase: 'ready',
        message: `配音已就绪：${status.pagePngKeys.length > 0 ? '含页面图' : '页面渲染未就绪（音频+字幕可播）'}`
      });
    } catch (error) {
      let message = error instanceof Error ? error.message : '配音生成失败';
      if (error instanceof ConnectError && error.code === 'resource_exhausted') {
        const est = narrationEstimate != null ? Math.round(narrationEstimate / 60) : null;
        message = est
          ? `生成额度不足（预计还需 ${est} 分钟）。可减少页面后重试，或联系管理员调整额度。`
          : '生成额度不足。可减少页面后重试，或联系管理员调整额度。';
      }
      setNarrationStatus({ phase: 'error', message });
    }
  };

  const exportManifest = async () => {
    if (!realManifest || exporting) return;
    setExporting(true);
    setNarrationStatus({ phase: 'idle', message: '导出为异步任务：创建导出任务后可在任务中心查看进度。' });
    try {
      const pagePngKeys = realManifest.resources
        .filter((resource) => resource.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG')
        .map((resource) => resource.key);
      const result = await createExport(identity, {
        projectId,
        format: 'ARTIFACT_FORMAT_WEB_PROJECT',
        timelineKey: realManifest.timelineKey,
        pagePngKeys,
        includeNotes: true
      });
      setNarrationStatus({ phase: 'ready', message: `导出任务已入队：${result.jobId}。完成后可在任务中心下载。` });
    } catch (error) {
      setNarrationStatus({ phase: 'error', message: error instanceof Error ? error.message : '导出失败' });
    } finally {
      setExporting(false);
    }
  };

  const pageCount = isReady ? slidesState.slides.length : 0;
  const scriptReadyCount = isReady ? Object.keys(realScripts).length : 0;
  const statusMarker = useMemo(() => {
    if (!isReady) return '等待解析';
    if (scriptReadyCount < pageCount) return `${scriptReadyCount}/${pageCount} 页讲稿`;
    return narrationStatus.phase === 'ready' ? '配音已就绪' : narrationStatus.phase === 'generating' ? '配音生成中' : '讲稿就绪，可配音';
  }, [isReady, scriptReadyCount, pageCount, narrationStatus]);

  return (
    <div className="workspace-v2">
      <header className="editor-header">
        <div className="editor-header-left">
          <Link to="/projects" className="back-link" title="返回项目列表">
            ← 项目列表
          </Link>
          <div>
            <span className="eyebrow">项目工程</span>
            <h1 title={projectId}>{projectId.slice(0, 12)}</h1>
          </div>
        </div>
        <div className="editor-header-right">
          <span className={`status-marker ${isReady ? '' : 'muted'}`}>{statusMarker}</span>
          <Link to={`/projects/${projectId}/artifacts`} className="button-ghost" title="项目成品与版本">
            成品
          </Link>
        </div>
      </header>

      <div className="editor-layout">
        <aside className="slide-rail-v2" aria-label="页面列表">
          <div className="rail-title">
            页面 <span className="muted-count">{pageCount || ''}</span>
          </div>
          {!isReady ? (
            <p className="empty-state">{slidesState.mode === 'loading' ? '加载中…' : '尚未解析页面：请先导入 PPTX，处理完成后自动出现页面列表。'}</p>
          ) : (
            slidesState.slides.map((slide, index) => (
              <button
                key={slide.slideId}
                type="button"
                className={slide.slideId === activeSlideID ? 'selected' : ''}
                onClick={() => setActiveSlideID(slide.slideId)}
              >
                <span>{String(index + 1).padStart(2, '0')}</span>
                <strong className="nowrap-ellipsis">{slide.title || slide.slideId}</strong>
                <em className="nowrap-ellipsis">{slide.preview || (slide.hasNotes ? '有备注' : '')}</em>
              </button>
            ))
          )}
        </aside>

        <section className="script-column">
          {activeRealScript ? (
            <ScriptEditor
              script={activeRealScript}
              onChange={(next) => setRealScripts((current) => ({ ...current, [next.slideId]: next }))}
              commit={commitRealScript}
              onCommitError={commitError}
            />
          ) : (
            <section className="editor-card">
              <header>
                <div>
                  <span className="eyebrow">讲稿</span>
                  <h2>{activeSlideID || '讲稿'}</h2>
                </div>
              </header>
              {isReady ? (
                <>
                  <p className="empty-state">
                    {draftStatus.message || '解析完成。选择讲稿模式后生成逐页讲稿，生成后可直接在此编辑。'}
                  </p>
                  <div className="draft-options" aria-label="讲稿模式">
                    {scriptModeOptions.map((option) => (
                      <label key={option.value} className={draftMode === option.value ? 'selected' : ''}>
                        <input type="radio" name="draft-mode" value={option.value} checked={draftMode === option.value} onChange={() => setDraftMode(option.value)} />
                        <span>{option.label}</span>
                        <small>{option.description}</small>
                      </label>
                    ))}
                  </div>
                  <div className="draft-actions">
                    <button type="button" disabled={draftStatus.phase === 'generating'} onClick={() => void generateAll()}>
                      {draftStatus.phase === 'generating'
                        ? '生成中…'
                        : draftRequested
                          ? '开始生成讲稿'
                          : `生成${modeLabel(draftMode)}`}
                    </button>
                  </div>
                </>
              ) : (
                <p className="empty-state">尚未完成解析，无法生成讲稿。</p>
              )}
            </section>
          )}
        </section>

        <aside className="properties-column">
          <section className="panel nested">
            <span className="eyebrow">配音属性</span>
            <label className="field-label">
              音色
              <select value={voiceId} onChange={(e) => setVoiceId(e.currentTarget.value)}>
                {voiceOptions.map((voice) => (
                  <option key={voice} value={voice}>
                    {voice}
                  </option>
                ))}
              </select>
            </label>
            <label className="field-label">
              语速 · {ratePercent}%
              <select value={ratePercent} onChange={(e) => setRatePercent(Number(e.currentTarget.value))}>
                {[75, 90, 100, 110, 125, 150].map((rate) => (
                  <option key={rate} value={rate}>
                    {rate === 100 ? '标准 100%' : `${rate}%`}
                  </option>
                ))}
              </select>
            </label>
            <div className="draft-actions">
              <button
                type="button"
                className="primary"
                disabled={!isReady || narrationStatus.phase === 'generating'}
                onClick={() => void generateNarration()}
              >
                {narrationStatus.phase === 'generating' ? '配音生成中…' : realManifest ? '重新生成配音' : '生成配音'}
              </button>
            </div>
            {narrationStatus.message && (
              <p className={`narration-note ${narrationStatus.phase === 'error' ? 'error' : ''}`}>{narrationStatus.message}</p>
            )}
            {narrationEstimate != null && (
              <p className="narration-note">预计生成时长 ≈ {Math.round(narrationEstimate / 60)} 分钟。</p>
            )}
          </section>

          {isReady && (
            <section className="panel nested">
              <span className="eyebrow">页面预览</span>
              {activeRealScript ? (
                <p className="page-preview-text">{activeRealScript.segments.map((segment) => segment.displayText).join('\n\n')}</p>
              ) : (
                <p className="page-preview-text muted">
                  {(slidesState.mode === 'real' && slidesState.slides.find((slide) => slide.slideId === activeSlideID)?.preview) ?? '预览渲染图就绪后展示。'}
                </p>
              )}
            </section>
          )}
        </aside>
      </div>

      <section className="playback-column">
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
        {realManifest ? (
          <>
            <Player manifest={realManifest} onSlideChange={setActiveSlideID} />
            <div className="export-row">
              <button type="button" className="button-primary" disabled={exporting} onClick={() => void exportManifest()}>
                {exporting ? '创建导出任务中…' : '导出为 Web 工程'}
              </button>
              <Link to={`/projects/${projectId}/artifacts`} className="button-ghost">
                查看成品与版本
              </Link>
            </div>
          </>
        ) : (
          <section className="player-card">
            <span className="eyebrow">播放器预览</span>
            <p className="empty-state">
              {narrationStatus.message || '生成配音后即可在此试听音频与字幕；页面图渲染就绪时同时展示幻灯片。'}
            </p>
          </section>
        )}
      </section>
    </div>
  );
}