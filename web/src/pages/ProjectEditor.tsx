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
import { useI18n } from '../i18n';
import { Link } from '../router';
import type { PlaybackManifest, ScriptMode, ScriptRevision, ScriptSegment, SlideSummary } from '../types';

type SlidesState =
  | { mode: 'loading' }
  | { mode: 'empty' }
  | { mode: 'real'; slides: SlideSummary[]; revisionNo: number };

type DraftStatus = { phase: 'idle' | 'generating' | 'ready' | 'error'; message: string };
type ConflictState = { slideId: string; localText: string; latest: ScriptRevision } | null;

const scriptModeOptions: Array<{ value: ScriptMode; labelKey: string; descKey: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', labelKey: 'editor.modes.original', descKey: 'editor.modes.originalDesc' },
  { value: 'SCRIPT_MODE_POLISH', labelKey: 'editor.modes.polish', descKey: 'editor.modes.polishDesc' },
  { value: 'SCRIPT_MODE_AI_GENERATED', labelKey: 'editor.modes.ai', descKey: 'editor.modes.aiDesc' }
];

const modeLabel = (mode: ScriptMode | undefined, t: (key: string) => string) =>
  t(scriptModeOptions.find((item) => item.value === mode)?.labelKey ?? 'editor.modes.original');

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

const devNarrationVoiceID = 'fake-voice-1';

export function ProjectEditor({ identity, projectId, draftRequested }: { identity: ClientIdentity; projectId: string; draftRequested?: boolean }) {
  const { t } = useI18n();
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
        throw new Error(t('editor.conflict.default'));
      }
      return result.revision;
    },
    [identity, projectId, activeSlideID, t]
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
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.saveFailed') });
    }
  };

  const commitError = (message: string) => {
    setDraftStatus({ phase: 'error', message });
  };

  const generateAll = async () => {
    if (!isReady || draftStatus.phase === 'generating') return;
    setDraftStatus({ phase: 'generating', message: t('editor.generateQueued', { mode: modeLabel(draftMode, t) }) });
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
          ? { phase: 'ready', message: t('editor.generateDone', { count: slidesState.slides.length }) }
          : { phase: 'error', message: t('editor.generateBackground') }
      );
    } catch (error) {
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.generateFailed') });
    }
  };

  const generateNarration = async () => {
    if (!isReady || narrationStatus.phase === 'generating') return;
    if (Object.keys(realScripts).length < slidesState.slides.length) {
      setNarrationStatus({ phase: 'error', message: t('editor.needAllScripts') });
      return;
    }
    const selectedVoice = voiceId || devNarrationVoiceID;
    setNarrationStatus({ phase: 'generating', message: t('editor.narrationQueued') });
    try {
      const slideIds = slidesState.slides.map((slide) => slide.slideId);
      try {
        const est = await estimateNarration(identity, projectId, slideIds, selectedVoice);
        setNarrationEstimate(est.estimatedSeconds);
        setNarrationStatus({
          phase: 'generating',
          message: t('editor.narrationEstimate', { minutes: Math.round(est.estimatedSeconds / 60) })
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
        setNarrationStatus({ phase: 'error', message: t('editor.narrationBackground') });
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
        message: status.pagePngKeys.length > 0 ? t('editor.narrationReadyImages') : t('editor.narrationReadyNoImages')
      });
    } catch (error) {
      let message = error instanceof Error ? error.message : t('editor.narrationFailed');
      if (error instanceof ConnectError && error.code === 'resource_exhausted') {
        const est = narrationEstimate != null ? Math.round(narrationEstimate / 60) : null;
        message = est ? t('editor.quotaShort', { minutes: est }) : t('editor.quotaShortNoEst');
      }
      setNarrationStatus({ phase: 'error', message });
    }
  };

  const exportManifest = async () => {
    if (!realManifest || exporting) return;
    setExporting(true);
    setNarrationStatus({ phase: 'idle', message: t('editor.exportQueued') });
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
      setNarrationStatus({ phase: 'ready', message: t('editor.exportQueuedId', { jobId: result.jobId }) });
    } catch (error) {
      setNarrationStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.exportFailed') });
    } finally {
      setExporting(false);
    }
  };

  const pageCount = isReady ? slidesState.slides.length : 0;
  const scriptReadyCount = isReady ? Object.keys(realScripts).length : 0;
  const statusMarker = useMemo(() => {
    if (!isReady) return t('editor.status.waitingParse');
    if (scriptReadyCount < pageCount) return t('editor.status.scripts', { ready: scriptReadyCount, total: pageCount });
    return narrationStatus.phase === 'ready'
      ? t('editor.status.narrationReady')
      : narrationStatus.phase === 'generating'
        ? t('editor.status.narrationGenerating')
        : t('editor.status.scriptReady');
  }, [isReady, scriptReadyCount, pageCount, narrationStatus, t]);

  return (
    <div className="workspace-v2">
      <header className="editor-header">
        <div className="editor-header-left">
          <Link to="/projects" className="back-link" title={t('editor.backToProjectsTitle')}>
            {t('editor.backToProjects')}
          </Link>
          <div>
            <span className="eyebrow">{t('editor.project')}</span>
            <h1 title={projectId}>{projectId.slice(0, 12)}</h1>
          </div>
        </div>
        <div className="editor-header-right">
          <span className={`status-marker ${isReady ? '' : 'muted'}`}>{statusMarker}</span>
          <Link to={`/projects/${projectId}/artifacts`} className="button-ghost" title={t('editor.artifactsTitle')}>
            {t('editor.artifacts')}
          </Link>
        </div>
      </header>

      <div className="editor-layout">
        <aside className="slide-rail-v2" aria-label={t('editor.pageList')}>
          <div className="rail-title">
            {t('editor.pages')} <span className="muted-count">{pageCount || ''}</span>
          </div>
          {!isReady ? (
            <p className="empty-state">{slidesState.mode === 'loading' ? t('editor.loading') : t('editor.noSlides')}</p>
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
                <em className="nowrap-ellipsis">{slide.preview || (slide.hasNotes ? t('editor.hasNotes') : '')}</em>
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
                  <span className="eyebrow">{t('editor.scriptEyebrow')}</span>
                  <h2>{activeSlideID || t('editor.scriptEyebrow')}</h2>
                </div>
              </header>
              {isReady ? (
                <>
                  <p className="empty-state">
                    {draftStatus.message || t('editor.draftHint')}
                  </p>
                  <div className="draft-options" aria-label={t('editor.scriptMode')}>
                    {scriptModeOptions.map((option) => (
                      <label key={option.value} className={draftMode === option.value ? 'selected' : ''}>
                        <input type="radio" name="draft-mode" value={option.value} checked={draftMode === option.value} onChange={() => setDraftMode(option.value)} />
                        <span>{t(option.labelKey)}</span>
                        <small>{t(option.descKey)}</small>
                      </label>
                    ))}
                  </div>
                  <div className="draft-actions">
                    <button type="button" disabled={draftStatus.phase === 'generating'} onClick={() => void generateAll()}>
                      {draftStatus.phase === 'generating'
                        ? t('editor.generating')
                        : draftRequested
                          ? t('editor.startGenerate')
                          : t('editor.generateMode', { mode: modeLabel(draftMode, t) })}
                    </button>
                  </div>
                </>
              ) : (
                <p className="empty-state">{t('editor.notParsed')}</p>
              )}
            </section>
          )}
        </section>

        <aside className="properties-column">
          <section className="panel nested">
            <span className="eyebrow">{t('editor.narrationProps')}</span>
            <label className="field-label">
              {t('editor.voice')}
              <select value={voiceId} onChange={(e) => setVoiceId(e.currentTarget.value)}>
                {voiceOptions.map((voice) => (
                  <option key={voice} value={voice}>
                    {voice}
                  </option>
                ))}
              </select>
            </label>
            <label className="field-label">
              {t('editor.rate', { percent: ratePercent })}
              <select value={ratePercent} onChange={(e) => setRatePercent(Number(e.currentTarget.value))}>
                {[75, 90, 100, 110, 125, 150].map((rate) => (
                  <option key={rate} value={rate}>
                    {rate === 100 ? t('editor.rateStandard') : `${rate}%`}
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
                {narrationStatus.phase === 'generating' ? t('editor.narrationGeneratingBtn') : realManifest ? t('editor.regenerateNarration') : t('editor.generateNarration')}
              </button>
            </div>
            {narrationStatus.message && (
              <p className={`narration-note ${narrationStatus.phase === 'error' ? 'error' : ''}`}>{narrationStatus.message}</p>
            )}
            {narrationEstimate != null && (
              <p className="narration-note">{t('editor.estimatedDuration', { minutes: Math.round(narrationEstimate / 60) })}</p>
            )}
          </section>

          {isReady && (
            <section className="panel nested">
              <span className="eyebrow">{t('editor.pagePreview')}</span>
              {activeRealScript ? (
                <p className="page-preview-text">{activeRealScript.segments.map((segment) => segment.displayText).join('\n\n')}</p>
              ) : (
                <p className="page-preview-text muted">
                  {(slidesState.mode === 'real' && slidesState.slides.find((slide) => slide.slideId === activeSlideID)?.preview) ?? t('editor.previewPending')}
                </p>
              )}
            </section>
          )}
        </aside>
      </div>

      <section className="playback-column">
        {conflict && (
          <section className="compare-panel" aria-label={t('editor.conflictAria')}>
            <header>
              <span className="eyebrow">{t('editor.conflictEyebrow')}</span>
              <h3>{conflict.slideId}</h3>
            </header>
            <div className="compare-columns">
              <div>
                <h4>{t('editor.myChanges')}</h4>
                <pre>{conflict.localText}</pre>
              </div>
              <div>
                <h4>{t('editor.serverLatest', { revision: conflict.latest.revision })}</h4>
                <pre>{conflict.latest.segments.map((segment) => segment.displayText).join('\n\n')}</pre>
              </div>
            </div>
            <div className="draft-actions">
              <button type="button" onClick={() => void retryWithLatest()}>
                {t('editor.retryMine')}
              </button>
              <button type="button" onClick={acceptLatest}>
                {t('editor.acceptServer')}
              </button>
            </div>
          </section>
        )}
        {realManifest ? (
          <>
            <Player manifest={realManifest} onSlideChange={setActiveSlideID} />
            <div className="export-row">
              <button type="button" className="button-primary" disabled={exporting} onClick={() => void exportManifest()}>
                {exporting ? t('editor.exporting') : t('editor.exportWeb')}
              </button>
              <Link to={`/projects/${projectId}/artifacts`} className="button-ghost">
                {t('editor.viewArtifacts')}
              </Link>
            </div>
          </>
        ) : (
          <section className="player-card">
            <span className="eyebrow">{t('editor.playerPreview')}</span>
            <p className="empty-state">
              {narrationStatus.message || t('editor.playerHint')}
            </p>
          </section>
        )}
      </section>
    </div>
  );
}