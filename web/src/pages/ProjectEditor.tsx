import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ConnectError,
  approveScript,
  createDictionary,
  createExport,
  createGeneration,
  estimateNarration,
  generateDraft,
  getNarration,
  getNarrationDraftCount,
  getPlaybackManifest,
  getProjectSlides,
  getScript,
  getSlideRenderURLs,
  getSlideScriptSources,
  listGateways,
  listJobsPage,
  lockScript,
  publishWork,
  regenerateSegments,
  setSlideScriptSource,
  updateScript as updateScriptApi,
  type ClientIdentity,
  type SlideScriptSource
} from '../api';
import { Player } from '../Player';
import { ScriptEditor, type ScriptEditorHandle, type ScriptEditorStatus } from '../ScriptEditor';
import { ExportDialog, type ExportOptions } from '../components/ExportDialog';
import { useI18n } from '../i18n';
import { Link } from '../router';
import type { ArtifactFormat, Job, PlaybackManifest, Role, ScriptMode, ScriptRevision, ScriptSegment, SlideSummary } from '../types';

type SlidesState =
  | { mode: 'loading' }
  | { mode: 'empty' }
  | { mode: 'real'; slides: SlideSummary[]; revisionNo: number };

type DraftStatus = { phase: 'idle' | 'generating' | 'ready' | 'error'; message: string };
type ConflictState = { slideId: string; localText: string; latest: ScriptRevision } | null;

// B3-M5：生成类任务（配音生成 / 讲稿生成）的活跃态判定，用于"生成中继续编辑"顶部快照提示。
const genJobKinds = ['narration', 'script_draft'];
const genActiveStates = [
  'JOB_STATE_QUEUED',
  'JOB_STATE_RUNNING',
  'JOB_STATE_RETRY_WAIT',
  'JOB_STATE_CANCEL_REQUESTED',
  'JOB_STATE_UNKNOWN_PROVIDER_RESULT'
];

const scriptModeOptions: Array<{ value: ScriptMode; labelKey: string; descKey: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', labelKey: 'editor.modes.original', descKey: 'editor.modes.originalDesc' },
  { value: 'SCRIPT_MODE_POLISH', labelKey: 'editor.modes.polish', descKey: 'editor.modes.polishDesc' },
  { value: 'SCRIPT_MODE_AI_GENERATED', labelKey: 'editor.modes.ai', descKey: 'editor.modes.aiDesc' }
];

const modeLabel = (mode: ScriptMode | undefined, t: (key: string) => string) =>
  t(scriptModeOptions.find((item) => item.value === mode)?.labelKey ?? 'editor.modes.original');

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

const devNarrationVoiceID = 'fake-voice-1';

export function ProjectEditor({ identity, projectId, draftRequested, role }: { identity: ClientIdentity; projectId: string; draftRequested?: boolean; role?: Role }) {
  const { t } = useI18n();
  const [slidesState, setSlidesState] = useState<SlidesState>({ mode: 'loading' });
  const [activeSlideID, setActiveSlideID] = useState('');
  const [realScripts, setRealScripts] = useState<Record<string, ScriptRevision>>({});
  const [draftStatus, setDraftStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [draftMode, setDraftMode] = useState<ScriptMode>('SCRIPT_MODE_POLISH');
  const [narrationStatus, setNarrationStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [realManifest, setRealManifest] = useState<PlaybackManifest | null>(null);
  const [conflict, setConflict] = useState<ConflictState>(null);
  const [narrationEstimate, setNarrationEstimate] = useState<NarrationEstimate | null>(null);
  const [voiceOptions, setVoiceOptions] = useState<string[]>([]);
  const [voiceId, setVoiceId] = useState('');
  const [ratePercent, setRatePercent] = useState(100);
  // M4 ⑦：生成范围 + 待确认稿数（C-5 前置检查）。
  const [genScope, setGenScope] = useState<'all' | 'pending' | 'current'>('all');
  const [draftSegments, setDraftSegments] = useState(0);
  const [exporting, setExporting] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const [exportError, setExportError] = useState('');
  const [pubOpen, setPubOpen] = useState(false);
  const [pubTitle, setPubTitle] = useState('');
  const [pubSummary, setPubSummary] = useState('');
  const [pubStatus, setPubStatus] = useState<{ phase: 'idle' | 'submitting' | 'done' | 'error'; message: string }>({ phase: 'idle', message: '' });
  // B2 M2：真实渲染缩略图 / 属性抽屉 / 未保存状态。
  const [renderUrls, setRenderUrls] = useState<Record<string, string>>({});
  const [propsOpen, setPropsOpen] = useState(false);
  const [unsaved, setUnsaved] = useState(false);
  const scriptEditorRef = useRef<ScriptEditorHandle>(null);
  // M3 ③：确认/锁定需 REVIEWER 及以上（rank>=1，即 reviewer/editor/admin/owner）。
  const canReview = role !== undefined && role !== 'ROLE_VIEWER';
  // M3 ②：正在局部重生成的段落（按当前页 segmentId）。
  const [regeneratingIds, setRegeneratingIds] = useState<string[]>([]);
  // M3 ⑥：无备注页讲稿来源选择（持久化）。
  const [slideSources, setSlideSources] = useState<Record<string, SlideScriptSource>>({});
  const [dictNotice, setDictNotice] = useState<string>('');

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

  // 真实渲染缩略图 / 预览图：读取每页渲染 PNG 的短期签名 URL（按 slideId 对齐）；失败则降级为序号/标题缩略图。
  useEffect(() => {
    if (slidesState.mode !== 'real') {
      setRenderUrls({});
      return;
    }
    let cancelled = false;
    getSlideRenderURLs(identity, projectId)
      .then((res) => {
        if (cancelled) return;
        const map: Record<string, string> = {};
        for (const item of res.slides) map[item.slideId] = item.url;
        setRenderUrls(map);
      })
      .catch(() => {
        // 渲染图不可用（解析未完成 / 端点未就绪），保持空映射，前端降级展示。
      });
    return () => {
      cancelled = true;
    };
  }, [slidesState, identity, projectId]);

  // M3 ⑥：加载项目内"无备注页讲稿来源"选择（持久化，供生成草稿时 Worker 尊重）。
  useEffect(() => {
    if (slidesState.mode !== 'real') {
      setSlideSources({});
      return;
    }
    let cancelled = false;
    getSlideScriptSources(identity, projectId)
      .then((res) => {
        if (cancelled) return;
        const map: Record<string, SlideScriptSource> = {};
        for (const item of res.sources) map[item.slideId] = item;
        setSlideSources(map);
      })
      .catch(() => {
        // 端点未就绪（store 未配置）或权限不足，保持空映射，来源选择降级为不可见。
      });
    return () => {
      cancelled = true;
    };
  }, [slidesState, identity, projectId]);

  // M4 ⑦：拉取待确认稿数（draft 状态分段总数），供生成面板 C-5 前置检查。
  useEffect(() => {
    if (slidesState.mode !== 'real') {
      setDraftSegments(0);
      return;
    }
    let cancelled = false;
    getNarrationDraftCount(identity, projectId)
      .then((res) => {
        if (!cancelled) setDraftSegments(res.draftSegments);
      })
      .catch(() => {
        // 端点未就绪，保持 0（不阻止生成）。
      });
    return () => {
      cancelled = true;
    };
  }, [slidesState, identity, projectId]);

  // B3-M5：感知本项目活跃的生成任务（配音/讲稿），驱动"生成中继续编辑"顶部快照提示。
  // 生成任务以创建时已确认的讲稿快照为输入（C-5：lockConfirmedOnly），故生成期间仍可继续编辑，
  // 新改动需下一次生成才生效。此处用 5s 轮询（与任务中心断线回退频率一致），避免在编辑器内持有长连接。
  const refreshActiveGenJobs = useCallback(async () => {
    try {
      const page = await listJobsPage(identity, { projectId, pageSize: 20 });
      setActiveGenJobs(
        page.jobs.filter((job) => genJobKinds.includes(job.kind) && genActiveStates.includes(job.state))
      );
    } catch {
      // 任务服务不可用时保持上一次结果，不打扰编辑。
    }
  }, [identity, projectId]);

  useEffect(() => {
    void refreshActiveGenJobs();
    const timer = window.setInterval(() => void refreshActiveGenJobs(), 5000);
    return () => window.clearInterval(timer);
  }, [refreshActiveGenJobs]);

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

  // B2 M2 ⑧：Ctrl/Cmd+S 立即保存当前页草稿（阻止浏览器保存网页）。
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && (event.key === 's' || event.key === 'S')) {
        event.preventDefault();
        scriptEditorRef.current?.flush();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // B2 M2 ⑧：存在未保存稿时，离开页面前提示。
  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      if (unsaved) {
        event.preventDefault();
        event.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, [unsaved]);

  const isReady = slidesState.mode === 'real';
  const activeSlide = isReady ? slidesState.slides.find((slide) => slide.slideId === activeSlideID) : undefined;
  const activeRenderURL = activeSlide ? renderUrls[activeSlide.slideId] : undefined;
  const activeRealScript = isReady ? realScripts[activeSlideID] : undefined;
  const activeIndex = isReady ? slidesState.slides.findIndex((slide) => slide.slideId === activeSlideID) : -1;

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

  // B2 M2 ⑧：切换页面前先刷新（提交）当前页待保存队列，避免丢失未保存编辑。
  const handleSlideSelect = (slideId: string) => {
    if (slideId === activeSlideID) return;
    scriptEditorRef.current?.flush();
    setActiveSlideID(slideId);
  };

  // M3 ③：确认当前页讲稿（REVIEWER 及以上）。
  const approveActive = useCallback(async () => {
    if (!isReady || !activeRealScript) return;
    try {
      const rev = await approveScript(identity, projectId, activeSlideID);
      setRealScripts((current) => ({ ...current, [activeSlideID]: rev }));
    } catch (error) {
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.approveFailed') });
    }
  }, [identity, projectId, activeSlideID, isReady, activeRealScript, t]);

  // M3 ③：锁定当前页讲稿（后端不支持解锁）。
  const lockActive = useCallback(async () => {
    if (!isReady || !activeRealScript) return;
    try {
      const rev = await lockScript(identity, projectId, activeSlideID);
      setRealScripts((current) => ({ ...current, [activeSlideID]: rev }));
    } catch (error) {
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.lockFailed') });
    }
  }, [identity, projectId, activeSlideID, isReady, activeRealScript, t]);

  // M3 ②：局部重生成选中分段（RegenerateSegments）。轮询讲稿直到 revision 变化或超时后刷新。
  const regenerateActive = useCallback(
    async (segmentIds: string[]) => {
      if (!isReady || !activeRealScript || segmentIds.length === 0) return;
      setRegeneratingIds(segmentIds);
      try {
        await regenerateSegments(identity, projectId, activeSlideID, segmentIds, voiceId || undefined);
        const deadline = Date.now() + 120_000;
        let refreshed: ScriptRevision | undefined;
        while (Date.now() < deadline) {
          await sleep(1500);
          try {
            const rev = await getScript(identity, projectId, activeSlideID);
            if (rev.revision !== activeRealScript.revision) {
              refreshed = rev;
              break;
            }
          } catch {
            // 尚未就绪，继续轮询。
          }
        }
        if (refreshed) setRealScripts((current) => ({ ...current, [activeSlideID]: refreshed }));
      } catch (error) {
        setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.regenerateFailed') });
      } finally {
        setRegeneratingIds([]);
      }
    },
    [identity, projectId, activeSlideID, isReady, activeRealScript, voiceId, t]
  );

  // M3 ⑥：保存单页讲稿来源选择（无备注页显式指定驱动草稿来源）。
  const saveSlideSource = useCallback(
    async (slideId: string, source: string, customText = '') => {
      try {
        const saved = await setSlideScriptSource(identity, projectId, slideId, source, customText);
        setSlideSources((current) => ({ ...current, [slideId]: saved }));
      } catch (error) {
        setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.sourceSaveFailed') });
      }
    },
    [identity, projectId, t]
  );

  // M4 ⑤：读音调整弹窗「添加到租户词典」——构造单条规则（原词→读音）并写入当前租户发音词典。
  const addToDictionary = useCallback(
    async (word: string, reading: string) => {
      try {
        await createDictionary(identity, {
          name: t('editor.dictName', { word }),
          rules: [{ pattern: word, replacement: reading, enabled: true }]
        });
        setDictNotice(t('editor.dictAdded', { word }));
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        setDictNotice(`${t('editor.dictAddFailed')}：${message}`);
      }
    },
    [identity, t]
  );

  const handleScriptStatus = useCallback((status: ScriptEditorStatus) => {
    setUnsaved(status !== 'saved');
  }, []);

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

  // M4 ⑦：按范围计算参与配音的页（仅已有讲稿的页）。
  const scopeSlideIds = (): string[] => {
    if (genScope === 'current') return activeRealScript ? [activeSlideID] : [];
    return slidesState.slides.filter((slide) => realScripts[slide.slideId]).map((slide) => slide.slideId);
  };

  const generateNarration = async () => {
    if (!isReady || narrationStatus.phase === 'generating') return;
    // C-5 强制：存在未确认（draft）讲稿时阻止正式生成（后端亦校验 RequireConfirmed）。
    if (draftSegments > 0) {
      setNarrationStatus({ phase: 'error', message: t('editor.blockedByDraft', { count: draftSegments }) });
      return;
    }
    const slideIds = scopeSlideIds();
    if (slideIds.length === 0) {
      setNarrationStatus({ phase: 'error', message: t('editor.noScriptsInScope') });
      return;
    }
    const selectedVoice = voiceId || devNarrationVoiceID;
    setNarrationStatus({ phase: 'generating', message: t('editor.narrationQueued') });
    try {
      try {
        const est = await estimateNarration(identity, projectId, slideIds, selectedVoice, ratePercent);
        setNarrationEstimate(est);
        setNarrationStatus({
          phase: 'generating',
          message: t('editor.narrationEstimate', { minutes: Math.round(est.estimatedSeconds / 60) })
        });
      } catch {
        // 预估失败不阻塞生成。
      }
      const idempotencyKey = `narration-${projectId}-${Date.now()}`;
      await createGeneration(identity, projectId, slideIds, selectedVoice, idempotencyKey, {
        ratePercent,
        lockConfirmedOnly: true
      });
      // B3-M5：生成任务已创建，立即刷新活跃任务，让顶部快照提示尽快出现。
      void refreshActiveGenJobs();
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
      // B3-M5：生成完成后立即收敛提示（无需等下一次轮询）。
      void refreshActiveGenJobs();
    } catch (error) {
      let message = error instanceof Error ? error.message : t('editor.narrationFailed');
      if (error instanceof ConnectError && error.code === 'resource_exhausted') {
        const estSec = narrationEstimate != null ? Math.round(narrationEstimate.estimatedSeconds / 60) : null;
        message = estSec ? t('editor.quotaShort', { minutes: estSec }) : t('editor.quotaShortNoEst');
      }
      setNarrationStatus({ phase: 'error', message });
    }
  };

  const runExport = async (format: ArtifactFormat, options: ExportOptions) => {
    if (!realManifest || exporting) return;
    setExporting(true);
    setExportError('');
    setNarrationStatus({ phase: 'idle', message: t('editor.exportQueued') });
    try {
      const pagePngKeys = realManifest.resources
        .filter((resource) => resource.type === 'PLAYBACK_RESOURCE_TYPE_PAGE_PNG')
        .map((resource) => resource.key);
      const result = await createExport(identity, {
        projectId,
        format,
        timelineKey: realManifest.timelineKey,
        pagePngKeys: format === 'ARTIFACT_FORMAT_MP4' ? pagePngKeys : [],
        burnSubtitles: format === 'ARTIFACT_FORMAT_MP4' ? options.burnSubtitles : undefined,
        includeNotes: options.includeNotes,
        idempotencyKey: `export-${projectId}-${Date.now()}`
      });
      setNarrationStatus({ phase: 'ready', message: t('editor.exportQueuedId', { jobId: result.jobId }) });
      setExportOpen(false);
    } catch (error) {
      setExportError(error instanceof Error ? error.message : t('editor.exportFailed'));
    } finally {
      setExporting(false);
    }
  };

  const pageCount = isReady ? slidesState.slides.length : 0;
  const noNotesSlides = isReady ? slidesState.slides.filter((slide) => !slide.hasNotes) : [];
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

  const submitPublish = async () => {
    const title = pubTitle.trim();
    if (!title) {
      setPubStatus({ phase: 'error', message: t('public.publishTitleRequired') });
      return;
    }
    setPubStatus({ phase: 'submitting', message: '' });
    try {
      await publishWork(identity, { projectId, title, summary: pubSummary.trim() });
      setPubStatus({ phase: 'done', message: '' });
      setPubOpen(false);
      setPubTitle('');
      setPubSummary('');
    } catch (err) {
      setPubStatus({
        phase: 'error',
        message: err instanceof Error ? err.message : t('public.publishFailed')
      });
    }
  };

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
          <span className={`status-marker ${unsaved ? 'unsaved' : ''} ${isReady ? '' : 'muted'}`}>
            {unsaved ? t('editor.unsaved') : statusMarker}
          </span>
          <button type="button" className="button-ghost" onClick={() => setPropsOpen(true)} title={t('editor.propertiesTitle')}>
            {t('editor.properties')}
          </button>
          <Link to={`/projects/${projectId}/artifacts`} className="button-ghost" title={t('editor.artifactsTitle')}>
            {t('editor.artifacts')}
          </Link>
          <button type="button" className="button-ghost" onClick={() => setPubOpen(true)}>
            {t('public.publish')}
          </button>
        </div>
      </header>

      {activeGenJobs.length > 0 && (
        <div className="snapshot-banner" role="status" aria-live="polite">
          <span className="snapshot-dot" aria-hidden="true" />
          <div className="snapshot-text">
            <strong>{t('editor.genInProgress', { count: activeGenJobs.length })}</strong>
            <span>{t('editor.genSnapshotNote')}</span>
          </div>
          <Link to="/jobs" className="button-ghost">
            {t('editor.genViewJobs')}
          </Link>
        </div>
      )}

      <div className="editor-layout editor-layout-3col">
        <aside className="slide-rail-v2" aria-label={t('editor.pageList')}>
          <div className="rail-title">
            {t('editor.pages')} <span className="muted-count">{pageCount || ''}</span>
          </div>
          {!isReady ? (
            <p className="empty-state">{slidesState.mode === 'loading' ? t('editor.loading') : t('editor.noSlides')}</p>
          ) : (
            slidesState.slides.map((slide, index) => {
              const thumb = renderUrls[slide.slideId];
              return (
                <button
                  key={slide.slideId}
                  type="button"
                  className={slide.slideId === activeSlideID ? 'selected' : ''}
                  onClick={() => handleSlideSelect(slide.slideId)}
                >
                  <span className="thumb">
                    {thumb ? (
                      <img src={thumb} alt={slide.title || slide.slideId} loading="lazy" />
                    ) : (
                      String(index + 1).padStart(2, '0')
                    )}
                  </span>
                  <span className="thumb-meta">
                    <strong className="nowrap-ellipsis">{slide.title || slide.slideId}</strong>
                    <em className="nowrap-ellipsis">{slide.preview || (slide.hasNotes ? t('editor.hasNotes') : '')}</em>
                  </span>
                </button>
              );
            })
          )}
        </aside>

        <section className="slide-preview" aria-label={t('editor.slidePreview')}>
          {activeRenderURL ? (
            <img className="slide-preview-img" src={activeRenderURL} alt={activeSlide?.title ?? activeSlideID} />
          ) : (
            <div className="slide-preview-empty">{t('editor.renderPending')}</div>
          )}
          <div className="slide-preview-caption">
            <span>
              {activeIndex >= 0 ? `${activeIndex + 1} / ${pageCount}` : ''}
            </span>
            <strong className="nowrap-ellipsis">{activeSlide?.title}</strong>
          </div>
        </section>

        <section className="script-column">
          {activeRealScript ? (
            <ScriptEditor
              ref={scriptEditorRef}
              script={activeRealScript}
              onChange={(next) => setRealScripts((current) => ({ ...current, [next.slideId]: next }))}
              commit={commitRealScript}
              onCommitError={commitError}
              onStatusChange={handleScriptStatus}
              canReview={canReview}
              onApprove={approveActive}
              onLock={lockActive}
              regeneratingIds={regeneratingIds}
              onRegenerate={regenerateActive}
              onAddToDictionary={addToDictionary}
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
                  <p className="empty-state">{draftStatus.message || t('editor.draftHint')}</p>
                  <div className="draft-options" aria-label={t('editor.scriptMode')}>
                    {scriptModeOptions.map((option) => (
                      <label key={option.value} className={draftMode === option.value ? 'selected' : ''}>
                        <input type="radio" name="draft-mode" value={option.value} checked={draftMode === option.value} onChange={() => setDraftMode(option.value)} />
                        <span>{t(option.labelKey)}</span>
                        <small>{t(option.descKey)}</small>
                      </label>
                    ))}
                  </div>
                  {/* M3 ⑥：无备注页显式选择讲稿来源（生成草稿前设置，持久化后由 Worker 尊重）。 */}
                  {noNotesSlides.length > 0 && (
                    <div className="source-selector" aria-label={t('editor.noNotesSource')}>
                      <span className="eyebrow">{t('editor.chooseSource')}</span>
                      {noNotesSlides.map((slide) => {
                        const src = slideSources[slide.slideId]?.source ?? 'layout';
                        const custom = slideSources[slide.slideId]?.customText ?? '';
                        return (
                          <div key={slide.slideId} className="source-row">
                            <strong className="nowrap-ellipsis" title={slide.slideId}>
                              {slide.title || slide.slideId}
                            </strong>
                            <select
                              value={src}
                              onChange={(e) => {
                                const value = e.currentTarget.value;
                                if (value === 'custom') {
                                  // 自定义来源需先有文本再持久化（后端校验 customText 非空）；
                                  // 此处仅本地切换为 custom 以展示输入框，输入后由下方 onChange 持久化。
                                  setSlideSources((current) => ({
                                    ...current,
                                    [slide.slideId]: { slideId: slide.slideId, source: 'custom', customText: custom }
                                  }));
                                } else {
                                  saveSlideSource(slide.slideId, value);
                                }
                              }}
                            >
                              <option value="layout">{t('editor.sourceLayout')}</option>
                              <option value="title">{t('editor.sourceTitle')}</option>
                              <option value="body">{t('editor.sourceBody')}</option>
                              <option value="notes">{t('editor.sourceNotes')}</option>
                              <option value="custom">{t('editor.sourceCustom')}</option>
                            </select>
                            {src === 'custom' && (
                              <input
                                className="source-custom"
                                placeholder={t('editor.customSourcePlaceholder')}
                                value={custom}
                                onChange={(e) => {
                                  const text = e.currentTarget.value;
                                  if (text.trim() !== '') saveSlideSource(slide.slideId, 'custom', text);
                                  else setSlideSources((current) => ({ ...current, [slide.slideId]: { slideId: slide.slideId, source: 'custom', customText: '' } }));
                                }}
                              />
                            )}
                          </div>
                        );
                      })}
                    </div>
                  )}
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
      </div>

      {/* 属性面板：右上角按钮唤出的抽屉（B2 M2 ① 属性改页签/抽屉） */}
      {propsOpen && (
        <div className="drawer-backdrop" onClick={() => setPropsOpen(false)}>
          <aside className="properties-drawer" role="dialog" aria-label={t('editor.properties')} onClick={(event) => event.stopPropagation()}>
            <header>
              <span className="eyebrow">{t('editor.properties')}</span>
              <button type="button" onClick={() => setPropsOpen(false)}>
                {t('common.close')}
              </button>
            </header>
            <section className="panel nested">
              <span className="eyebrow">{t('editor.narrationProps')}</span>

              {/* M4 ④ 音色选择器（降级：后端无 voice catalog，无样例/语言/风格元数据；试听按钮禁用并提示）。 */}
              <div className="voice-selector" aria-label={t('editor.voiceSelector')}>
                <span className="field-label">{t('editor.voice')}</span>
                <div className="voice-cards">
                  {voiceOptions.map((voice) => (
                    <div key={voice} className={`voice-card ${voice === voiceId ? 'selected' : ''}`}>
                      <button type="button" className="voice-name" onClick={() => setVoiceId(voice)} title={voice}>
                        {voice}
                      </button>
                      <button type="button" className="voice-try" disabled title={t('editor.sampleUnavailable')}>
                        {t('editor.trySample')}
                      </button>
                    </div>
                  ))}
                </div>
              </div>

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

              {/* M4 ⑦ 生成面板：范围 / 待确认稿数 / 需新生成 / 用量 / C-5 阻止。 */}
              <div className="gen-panel" aria-label={t('editor.generatePanel')}>
                <span className="field-label">{t('editor.genScope')}</span>
                <div className="scope-options">
                  {(['all', 'current'] as const).map((scope) => (
                    <label key={scope} className={genScope === scope ? 'selected' : ''}>
                      <input type="radio" name="gen-scope" value={scope} checked={genScope === scope} onChange={() => setGenScope(scope)} />
                      <span>{t(`editor.scope.${scope}`)}</span>
                    </label>
                  ))}
                </div>

                <dl className="gen-stats">
                  <div>
                    <dt>{t('editor.draftCount')}</dt>
                    <dd className={draftSegments > 0 ? 'warn' : ''}>{draftSegments}</dd>
                  </div>
                  <div>
                    <dt>{t('editor.pendingCount')}</dt>
                    <dd>{Math.max(0, pageCount - scriptReadyCount)}</dd>
                  </div>
                  <div>
                    <dt>{t('editor.needGenerate')}</dt>
                    <dd>{genScope === 'current' ? (activeRealScript ? 0 : 1) : Math.max(0, pageCount - scriptReadyCount)}</dd>
                  </div>
                </dl>

                {narrationEstimate != null && (
                  <p className="narration-note">
                    {t('editor.estimatedDuration', { minutes: Math.round(narrationEstimate.estimatedSeconds / 60) })}
                    {' · '}
                    {t('editor.costRange', { min: narrationEstimate.costMin, max: narrationEstimate.costMax })}
                  </p>
                )}

                {draftSegments > 0 && (
                  <p className="form-error gen-blocked">{t('editor.blockedByDraftHint', { count: draftSegments })}</p>
                )}

                {dictNotice && (
                  <p className="narration-note dict-notice" role="status">{dictNotice}</p>
                )}

                <div className="draft-actions">
                  <button
                    type="button"
                    className="primary"
                    disabled={!isReady || narrationStatus.phase === 'generating' || draftSegments > 0}
                    onClick={() => void generateNarration()}
                  >
                    {narrationStatus.phase === 'generating' ? t('editor.narrationGeneratingBtn') : realManifest ? t('editor.regenerateNarration') : t('editor.generateNarration')}
                  </button>
                </div>
              </div>

              {narrationStatus.message && (
                <p className={`narration-note ${narrationStatus.phase === 'error' ? 'error' : ''}`}>{narrationStatus.message}</p>
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
      )}

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
            <Player manifest={realManifest} onSlideChange={handleSlideSelect} />
            <div className="export-row">
              <button
                type="button"
                className="button-primary"
                disabled={exporting}
                onClick={() => {
                  setExportError('');
                  setExportOpen(true);
                }}
              >
                {t('editor.export')}
              </button>
              <Link to={`/projects/${projectId}/artifacts`} className="button-ghost">
                {t('editor.viewArtifacts')}
              </Link>
            </div>
          </>
        ) : (
          <section className="player-card">
            <span className="eyebrow">{t('editor.playerPreview')}</span>
            <p className="empty-state">{narrationStatus.message || t('editor.playerHint')}</p>
          </section>
        )}
      </section>
      {exportOpen && realManifest && (
        <ExportDialog
          manifest={realManifest}
          busy={exporting}
          error={exportError}
          onClose={() => setExportOpen(false)}
          onSubmit={(format, options) => void runExport(format, options)}
        />
      )}
      {pubOpen && (
        <div className="modal-backdrop" role="dialog" aria-label={t('public.publishTitle')}>
          <section className="modal-card">
            <header>
              <div>
                <span className="eyebrow">{t('public.publish')}</span>
                <h2>{t('public.publishTitle')}</h2>
              </div>
              <button type="button" onClick={() => setPubOpen(false)}>
                {t('common.close')}
              </button>
            </header>
            <p className="muted">{t('public.publishSummary')}</p>
            <label className="field-label">
              {t('public.publishTitleLabel')}
              <input value={pubTitle} onChange={(e) => setPubTitle(e.currentTarget.value)} placeholder={t('public.publishTitleLabel')} />
            </label>
            <label className="field-label">
              {t('public.publishSummaryLabel')}
              <textarea value={pubSummary} onChange={(e) => setPubSummary(e.currentTarget.value)} rows={3} />
            </label>
            {pubStatus.phase === 'error' && <p className="form-error">{pubStatus.message}</p>}
            <div className="draft-actions">
              <button type="button" className="primary" disabled={pubStatus.phase === 'submitting'} onClick={() => void submitPublish()}>
                {pubStatus.phase === 'submitting' ? `${t('public.publishSubmit')}…` : t('public.publishSubmit')}
              </button>
              <button type="button" onClick={() => setPubOpen(false)}>
                {t('public.publishCancel')}
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
