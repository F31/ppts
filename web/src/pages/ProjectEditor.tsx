import type { PointerEvent as ReactPointerEvent } from 'react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ConnectError,
  createDictionary,
  createExport,
  createGeneration,
  estimateNarration,
  getNarrationStale,
  getJob,
  getJobDetail,
  getNarration,
  getPlaybackManifest,
  getVoiceSettings,
  getProject,
  getProjectSlides,
  getSourceRevisions,
  getRevisionDiff,
  getSlideNotes,
  setSlideNotes,
  type SourceRevisionSummary,
  type RevisionDiff,
  getScript,
  getSlideRenderURLs,
  getSlideScriptSources,
  listJobsPage,
  listProjectScripts,
  listVoiceModels,
  regenerateScriptDraft,
  regenerateSegments,
  saveVoiceSettings,
  setScriptLanguagePreference,
  setSlideScriptSource,
  updateScript as updateScriptApi,
  type ClientIdentity,
  type NarrationEstimate,
  type ProjectVoiceSettings,
  type SlideScriptSource,
  type VoiceModel
} from '../api';
import { describeApiError } from '../apiError';
import { Player } from '../Player';
import { can } from '../permissions';
import { ScriptConflictError, ScriptEditor, type ScriptEditorHandle, type ScriptEditorStatus } from '../ScriptEditor';
import { ExportDialog, type ExportOptions } from '../components/ExportDialog';
import { useI18n } from '../i18n';
import { Link, navigate, useRoute } from '../router';
import type { ArtifactFormat, Job, PlaybackManifest, Role, ScriptMode, ScriptRevision, ScriptSegment, SlideSummary } from '../types';
import { useDialogA11y } from '../a11y';

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

function manifestMatchesSlides(manifest: PlaybackManifest, slides: SlideSummary[]): boolean {
  const allowed = new Set(slides.map((slide) => slide.slideId));
  try {
    const timeline = JSON.parse(manifest.timelineJson) as { slides?: Array<{ slideId?: string }> };
    return (timeline.slides ?? []).every((slide) => Boolean(slide.slideId && allowed.has(slide.slideId)));
  } catch {
    return false;
  }
}

const scriptModeOptions: Array<{ value: ScriptMode; labelKey: string; descKey: string }> = [
  { value: 'SCRIPT_MODE_ORIGINAL', labelKey: 'editor.modes.original', descKey: 'editor.modes.originalDesc' },
  { value: 'SCRIPT_MODE_POLISH', labelKey: 'editor.modes.polish', descKey: 'editor.modes.polishDesc' },
  { value: 'SCRIPT_MODE_AI_GENERATED', labelKey: 'editor.modes.ai', descKey: 'editor.modes.aiDesc' }
];

const modeLabel = (mode: ScriptMode | undefined, t: (key: string) => string) =>
  t(scriptModeOptions.find((item) => item.value === mode)?.labelKey ?? 'editor.modes.original');

const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

const devNarrationVoiceID = 'fake-voice-1';

export function ProjectEditor({
  identity,
  projectId,
  draftRequested,
  openExport = false,
  revisionNo: initRevisionNo,
  role,
  roleReady = true
}: {
  identity: ClientIdentity;
  projectId: string;
  draftRequested?: boolean;
  openExport?: boolean;
  revisionNo?: number;
  role?: Role;
  roleReady?: boolean;
}) {
  const { t } = useI18n();
  const [slidesState, setSlidesState] = useState<SlidesState>({ mode: 'loading' });
  const [activeSlideID, setActiveSlideID] = useState('');
  // 右侧讲稿栏：可折叠为抽屉（默认展开）。
  const [scriptOpen, setScriptOpen] = useState(true);
  const [realScripts, setRealScripts] = useState<Record<string, ScriptRevision>>({});
  // scriptsLoaded：已有讲稿是否已加载完成。用于避免加载窗口内误判"该页没有讲稿"而自动补建。
  const [scriptsLoaded, setScriptsLoaded] = useState(false);
  // scriptsLoadError：批量拉取讲稿失败的原因（A26：不许把接口错误压成"这些页没有讲稿"）。
  const [scriptsLoadError, setScriptsLoadError] = useState<string | null>(null);
  const [draftStatus, setDraftStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [draftMode, setDraftMode] = useState<ScriptMode>('SCRIPT_MODE_POLISH');
  const [narrationStatus, setNarrationStatus] = useState<DraftStatus>({ phase: 'idle', message: '' });
  const [realManifest, setRealManifest] = useState<PlaybackManifest | null>(null);
  const [conflict, setConflict] = useState<ConflictState>(null);
  const [narrationEstimate, setNarrationEstimate] = useState<NarrationEstimate | null>(null);
  const [voiceId, setVoiceId] = useState('');
  const [ratePercent, setRatePercent] = useState(100);
  // 语音属性弹窗：可选模型（TTS 网关）与当前模型名，以及编辑中的草稿值。
  const [voiceModels, setVoiceModels] = useState<VoiceModel[]>([]);
  const [voiceModelName, setVoiceModelName] = useState('');
  const [voiceDraftModel, setVoiceDraftModel] = useState('');
  const [voiceDraftVoice, setVoiceDraftVoice] = useState('');
  const [voiceDraftRate, setVoiceDraftRate] = useState(100);
  const [voiceSaving, setVoiceSaving] = useState(false);
  const [voiceSaveError, setVoiceSaveError] = useState('');
  // 配音生成方式：incremental=只补未配音/讲稿已更新的页；full=全部重新生成并覆盖。
  const [voiceGenMode, setVoiceGenMode] = useState<'incremental' | 'full'>('incremental');
  const [voiceStaleIds, setVoiceStaleIds] = useState<string[]>([]);
  const [voiceStaleLoading, setVoiceStaleLoading] = useState(false);
  const exportQueryHandledRef = useRef(false);
  // 版本历史（P0 多版本查看）：列历史版本 + 抽屉预览，只读，不切换生效版本。
  const [revisions, setRevisions] = useState<SourceRevisionSummary[]>([]);
  const [currentRevision, setCurrentRevision] = useState(0);
  const [previewRev, setPreviewRev] = useState<number | null>(null);
  const [previewSlides, setPreviewSlides] = useState<SlideSummary[] | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [diffRevB, setDiffRevB] = useState<number | null>(null);
  const [diffResult, setDiffResult] = useState<RevisionDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  // 项目标题与当前 PPT 展示名（用于回退链接）
  const [projectTitle, setProjectTitle] = useState('');
  const [pptDisplayName, setPptDisplayName] = useState('');
  // 当前幻灯片备注编辑
  const [slideNotesText, setSlideNotesText] = useState('');
  const [notesSlideID, setNotesSlideID] = useState('');
  const [notesSaving, setNotesSaving] = useState(false);
  const [notesError, setNotesError] = useState('');
  const notesSaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;
    getSourceRevisions(identity, projectId)
      .then((r) => {
        if (cancelled) return;
        setRevisions(r.revisions);
        setCurrentRevision(r.currentRevision);
        const cur = r.revisions.find((rv) => rv.isCurrent);
        setPptDisplayName(cur?.displayName ?? '');
      })
      .catch(() => {});
    getProject(identity, projectId)
      .then((p) => {
        if (!cancelled) setProjectTitle(p.title);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [identity, projectId]);

  // 加载/保存当前幻灯片备注（切换幻灯片或修订号时自动加载，失焦时延迟保存）。
  const loadSlideNotes = useCallback(async (sid: string, revNo: number) => {
    setNotesError('');
    try {
      const notes = await getSlideNotes(identity, projectId, sid, revNo);
      setSlideNotesText(notes ?? '');
      setNotesSlideID(sid);
    } catch (err) {
      setNotesError(err instanceof Error ? err.message : '');
    }
  }, [identity, projectId]);

  const scheduleSaveNotes = useCallback((text: string, sid: string, revNo: number) => {
    if (notesSaveTimer.current) clearTimeout(notesSaveTimer.current);
    setNotesSaving(true);
    notesSaveTimer.current = setTimeout(async () => {
      try {
        await setSlideNotes(identity, projectId, sid, revNo, text);
        setNotesError('');
      } catch (err) {
        setNotesError(err instanceof Error ? err.message : t('editor.notesSaveFailed'));
      } finally {
        setNotesSaving(false);
      }
    }, 600);
  }, [identity, projectId, t]);

  const computeDiff = useCallback(
    async (revB: number) => {
      if (previewRev == null) return;
      setDiffRevB(revB);
      setDiffLoading(true);
      try {
        const d = await getRevisionDiff(identity, projectId, previewRev, revB);
        setDiffResult(d);
      } catch {
        setDiffResult(null);
      } finally {
        setDiffLoading(false);
      }
    },
    [identity, projectId, previewRev],
  );

  const openVersionPreview = useCallback(
    async (revNo: number) => {
      setPreviewRev(revNo);
      setPreviewLoading(true);
      try {
        const res = await getProjectSlides(identity, projectId, revNo);
        setPreviewSlides(res.slides);
      } catch {
        setPreviewSlides([]);
      } finally {
        setPreviewLoading(false);
      }
    },
    [identity, projectId]
  );
  // M4 ⑦：生成范围 + 待确认稿数（C-5 前置检查）。
  // 讲稿改动后是否待重新生成语音（用于讲稿栏提示）。
  const [voiceDirty, setVoiceDirty] = useState(false);
  // B3-M5：本项目活跃的生成任务（配音/讲稿），驱动顶部"生成中继续编辑"快照提示。
  // 修正：c5a723e 引入 refreshActiveGenJobs 时漏声明该 state 与 NarrationEstimate 类型导入，
  // 会导致 `npm run build`（tsc）失败，此处补齐。
  const [activeGenJobs, setActiveGenJobs] = useState<Job[]>([]);
  const [exporting, setExporting] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const [exportError, setExportError] = useState('');
  // B2 M2：真实渲染缩略图 / 属性抽屉 / 未保存状态。
  const [renderUrls, setRenderUrls] = useState<Record<string, string>>({});
  const [propsOpen, setPropsOpen] = useState(false);
  const propsDialogRef = useDialogA11y<HTMLElement>(() => setPropsOpen(false));
  const [oneDraftOpen, setOneDraftOpen] = useState(false);
  const oneDraftDialogRef = useDialogA11y<HTMLElement>(() => setOneDraftOpen(false));
  // 项目讲稿语言：'' = 浏览器默认；一键成稿选定语言后切换，使面板与生成的讲稿一致。
  const [scriptLanguage, setScriptLanguage] = useState<string>(() => {
    try {
      return window.localStorage.getItem(`ppts:script-language:${projectId}`) ?? '';
    } catch {
      return '';
    }
  });
  // 一键成稿完成后强制重载讲稿（语言未变时也要刷新）。
  const [scriptsRefreshNonce, setScriptsRefreshNonce] = useState(0);
  const [oneDraftLanguage, setOneDraftLanguage] = useState<'zh-CN' | 'en-US'>(() => {
    try {
      return window.localStorage.getItem(`ppts:script-language:${projectId}`) === 'en-US' ? 'en-US' : 'zh-CN';
    } catch {
      return 'zh-CN';
    }
  });
  const [oneDraftSource, setOneDraftSource] = useState<'notes_first' | 'page_content' | 'notes_only'>('notes_first');
  const [oneDraftMode, setOneDraftMode] = useState<ScriptMode>('SCRIPT_MODE_POLISH');
  const [oneDraftLength, setOneDraftLength] = useState<'brief' | 'standard' | 'detailed'>('standard');
  const [oneDraftStyle, setOneDraftStyle] = useState('专业正式');
  const [oneDraftAudience, setOneDraftAudience] = useState('通用听众');
  const [oneDraftOverwrite, setOneDraftOverwrite] = useState<'fill_empty' | 'overwrite_all'>('fill_empty');
  const [oneDraftRunning, setOneDraftRunning] = useState(false);
  const [oneDraftProgress, setOneDraftProgress] = useState({ done: 0, total: 0, message: '' });
  const [oneDraftJobId, setOneDraftJobId] = useState('');
  // 成稿逐页结果（来自任务步骤计数）。任务"成功"不等于"有页被更新"——
  // 「仅补空」可能把每一页都跳过，必须把两个数字分开显示，否则用户会以为来源选择没生效。
  const [oneDraftResult, setOneDraftResult] = useState<{ generated: number; skipped: number } | null>(null);
  const [oneDraftResultUnavailable, setOneDraftResultUnavailable] = useState(false);
  const [unsaved, setUnsaved] = useState(false);
  // 编辑区网格容器 + 讲稿栏宽度拖拽（CSS 变量 --script-panel-width，仅当前会话生效）。
  const editorLayoutRef = useRef<HTMLDivElement>(null);
  const [scriptResizing, setScriptResizing] = useState(false);
  const scriptEditorRef = useRef<ScriptEditorHandle>(null);
  const autoNotesDraftRef = useRef<Set<string>>(new Set());
  const [autoNotesFailed, setAutoNotesFailed] = useState<string[]>([]);
  // B4-M1 权限边界（A22）：能力判定统一走 permissions.can，逐条镜像服务端 requireRole。
  // 角色未解析完成时不渲染需要权限的按钮（避免闪现假能力）。
  const canEditScript = roleReady && can(role, 'script.edit'); // 讲稿编辑：EDITOR+（script.go:55,120）
  const canGenerate = roleReady && can(role, 'narration.generate'); // 配音生成：EDITOR+（narration.go:65,167）
  const canExport = roleReady && can(role, 'export.create'); // 导出：EDITOR+（export.go:39）

  useEffect(() => {
    if (!openExport || exportQueryHandledRef.current || !canExport || !realManifest) return;
    exportQueryHandledRef.current = true;
    setExportOpen(true);
  }, [openExport, canExport, realManifest]);

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
    setScriptsLoaded(false);
    setDraftStatus({ phase: 'idle', message: '' });
    setNarrationStatus({ phase: 'idle', message: '' });
    setRealManifest(null);
    getProjectSlides(identity, projectId, initRevisionNo)
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
  }, [identity, projectId, initRevisionNo]);

  // 打开编辑页时加载已有配音：narration 就绪则构建播放清单，供中间预览区播放器直接播放。
  useEffect(() => {
    if (slidesState.mode !== 'real') {
      setRealManifest(null);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const status = await getNarration(identity, projectId);
        if (cancelled || !status.ready || !status.timelineKey) return;
        if (status.revisionNo !== slidesState.revisionNo) {
          setRealManifest(null);
          return;
        }
        const manifest = await getPlaybackManifest({
          identity,
          projectId,
          timelineKey: status.timelineKey,
          pagePngKeys: status.pagePngKeys ?? [],
          ttlSeconds: 900
        });
        if (cancelled) return;
        if (!manifestMatchesSlides(manifest, slidesState.slides)) {
          setRealManifest(null);
          return;
        }
        setRealManifest(manifest);
        setNarrationStatus({
          phase: 'ready',
          message: (status.pagePngKeys?.length ?? 0) > 0 ? t('editor.narrationReadyImages') : t('editor.narrationReadyNoImages')
        });
      } catch {
        // 尚未生成配音或端点未就绪：保持空态，不阻塞讲稿编辑。
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [slidesState, identity, projectId, t]);

  // 当前页备注：幻灯片或修订号变化时自动加载（含首次进入）。
  useEffect(() => {
    if (slidesState.mode !== 'real' || !activeSlideID) return;
    const revNo = slidesState.revisionNo || initRevisionNo || currentRevision;
    void loadSlideNotes(activeSlideID, revNo);
  }, [activeSlideID, slidesState, initRevisionNo, currentRevision, loadSlideNotes]);

  // 真实渲染缩略图 / 预览图：读取每页渲染 PNG 的短期签名 URL（按 slideId 对齐）；失败则降级为序号/标题缩略图。
  useEffect(() => {
    if (slidesState.mode !== 'real') {
      setRenderUrls({});
      return;
    }
    let cancelled = false;
    getSlideRenderURLs(identity, projectId, slidesState.revisionNo)
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

  // 先于数据请求同步讲稿语言偏好（effect 按声明顺序执行）。
  useEffect(() => {
    setScriptLanguagePreference(scriptLanguage || undefined);
  }, [scriptLanguage]);

  const persistScriptLanguage = (language: string) => {
    setScriptLanguage(language);
    try {
      window.localStorage.setItem(`ppts:script-language:${projectId}`, language);
    } catch {
      /* ignore */
    }
  };

  // 已有讲稿：**一次批量拉取**（GET /projects/{pid}/scripts），不再逐页串行 Get。
  //
  // 逐页串行是 O(N) 次往返，页数越多越明显：实测 38 页项目 5760ms，而批量端点 171ms（34×）。
  // 更关键的是 realScripts 直到循环结束才整体赋值，所以页数多的项目右侧讲稿区会**空白数秒**，
  // 期间点缩略图切页看起来"讲稿不跟着切"（2 页项目 ~300ms，不易察觉 —— 这正是"有项目差异"的原因）。
  // 批量端点还顺带避免了 N 次全量 revision 负载（每页都带回全部 segments）。
  //
  // 失败不静默（A26）：区分「这些页确实没有讲稿」（端点不返回该 slideId）与
  // 「加载失败」（请求出错 → 给原因 + 重试），否则"加载失败"会被渲染成"没有讲稿"。
  useEffect(() => {
    if (slidesState.mode !== 'real') return undefined;
    let cancelled = false;
    void (async () => {
      try {
        const { scripts } = await listProjectScripts(identity, projectId);
        if (cancelled) return;
        const found: Record<string, ScriptRevision> = {};
        // 端点按项目列出（含**其它版本残留**的 slideId，如上一版才有的页），
        // 必须按当前版本的页集过滤：realScripts 的键数被当作"已生成页数"用
        // （scriptReadyCount / 配音页数提示），全收会让 "已生成 N / M 页" 虚高。
        const allowed = new Set(slidesState.slides.map((slide) => slide.slideId));
        for (const rev of scripts) {
          if (rev.slideId && allowed.has(rev.slideId)) found[rev.slideId] = rev;
        }
        setRealScripts(found);
        setScriptsLoadError(null);
      } catch (error) {
        if (cancelled) return;
        setRealScripts({});
        setScriptsLoadError(describeApiError(error, t('editor.scriptsLoadFailed'), t));
      } finally {
        if (!cancelled) setScriptsLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [slidesState, identity, projectId, scriptLanguage, scriptsRefreshNonce, t]);

  // 语音属性：加载项目已保存的（模型/音色/语速）+ 可选模型与音色（来自 TTS 网关配置）。
  // 音色优先取接口返回；接口不可用或未配置音色时，退到开发音色并显式标注为"模拟音色"。
  useEffect(() => {
    let cancelled = false;
    (async () => {
      let models: VoiceModel[] = [];
      try {
        models = await listVoiceModels(identity, projectId);
      } catch {
        // 网关未启用/无权限：保持空列表，使用开发兜底。
      }
      let saved: ProjectVoiceSettings | null = null;
      try {
        saved = await getVoiceSettings(identity, projectId);
      } catch {
        // 端点不可用：使用缺省。
      }
      if (cancelled) return;
      setVoiceModels(models);

      const savedModel = saved?.model && models.some((m) => m.name === saved?.model) ? saved.model : '';
      const chosen = models.find((m) => m.name === savedModel) ?? models.find((m) => m.isDefault) ?? models[0];
      setVoiceModelName(chosen?.name ?? '');

      const available = chosen?.voices ?? [];
      const chosenVoice = saved?.voice && available.includes(saved.voice) ? saved.voice : available[0];
      setVoiceId(chosenVoice || saved?.voice || devNarrationVoiceID);
      setRatePercent(saved?.ratePercent && saved.ratePercent > 0 ? saved.ratePercent : 100);
    })();
    return () => {
      cancelled = true;
    };
  }, [identity, projectId]);

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
      // isDirty() 还覆盖"已切走的页仍有未落库草案"（R-13）：父级的 unsaved 只反映当前显示页的状态。
      if (unsaved || scriptEditorRef.current?.isDirty()) {
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
  // 当前页的讲稿来源选择（无备注页）。
  const activeSource = activeSlide ? slideSources[activeSlide.slideId]?.source ?? 'layout' : 'layout';
  const activeCustom = activeSlide ? slideSources[activeSlide.slideId]?.customText ?? '' : '';
  const canAutoCreateScriptFromNotes = Boolean(
    isReady &&
      activeSlideID &&
      activeSlide?.hasNotes &&
      !activeRealScript &&
      scriptsLoaded &&
      notesSlideID === activeSlideID &&
      slideNotesText.trim() &&
      !autoNotesFailed.includes(activeSlideID)
  );

  // 语音生成进度：来自对活跃任务的 5s 轮询，用于讲稿栏按钮后的进度/状态指示。
  const activeNarrationJob = activeGenJobs.find((job) => job.kind === 'narration');
  const voiceBusy = narrationStatus.phase === 'generating' || Boolean(activeNarrationJob);
  const voiceProgress = activeNarrationJob ? activeNarrationJob.progressPercent : -1;
  const voiceStatusText =
    activeNarrationJob &&
    (activeNarrationJob.state === 'JOB_STATE_QUEUED' || activeNarrationJob.state === 'JOB_STATE_RETRY_WAIT')
      ? t('editor.voiceProgressQueued')
      : t('editor.voiceProgressRunning');

  const oneDraftStorageKey = `ppts:one-draft:${projectId}`;

  // 成稿前的跳过预估（**估算**，非承诺值；后端逐页结果以任务步骤为准）。
  // slide.preview 由后端按「备注优先，其次首个非空形状文字」生成：
  //  - notes_only：「有备注」即可成稿，preview 不作判据；
  //  - 其余来源：preview 为空 ⇒ 备注与形状文字皆空 ⇒ 必然跳过；
  //    但 preview 非空**不保证**页面文字可抽取（仅备注页在 page_content 下仍会被后端跳过），
  //    故这类漏计只会让预估偏乐观，不会漏报真正会生成的页。
  // realScripts 只含**当前讲稿语言**的讲稿，故仅当生成语言与面板语言一致时跳过数才精确，
  // 否则标注为按目标语言的估算。
  const oneDraftPlan = useMemo(() => {
    if (slidesState.mode !== 'real') return null;
    const slides = slidesState.slides;
    let generate = 0;
    let skippedExisting = 0;
    let noText = 0;
    // withNotes 用于说明「打开有备注的页面会自动用备注建一版原文稿」——
    // 这会让「仅补空」在后续成稿时把这些页判为已有讲稿而跳过。
    let withNotes = 0;
    for (const slide of slides) {
      if (slide.hasNotes) withNotes += 1;
      const hasMaterial =
        oneDraftSource === 'notes_only' ? Boolean(slide.hasNotes) : Boolean((slide.preview ?? '').trim());
      if (!hasMaterial) {
        noText += 1;
        continue;
      }
      const hasScript = (realScripts[slide.slideId]?.segments.length ?? 0) > 0;
      if (hasScript && oneDraftOverwrite === 'fill_empty') {
        skippedExisting += 1;
        continue;
      }
      generate += 1;
    }
    return {
      generate,
      skippedExisting,
      noText,
      withNotes,
      total: slides.length,
      reliable: oneDraftLanguage === (scriptLanguage || 'zh-CN')
    };
  }, [slidesState, realScripts, oneDraftSource, oneDraftOverwrite, oneDraftLanguage, scriptLanguage]);

  useEffect(() => {
    const saved = window.localStorage.getItem(oneDraftStorageKey);
    if (saved) setOneDraftJobId(saved);
  }, [oneDraftStorageKey]);

  useEffect(() => {
    if (!oneDraftJobId) return;
    let cancelled = false;
    // 任务到达终态后必须停止轮询：此前定时器会一直跑（每 2s 一次 getJob + 全量 getScript），
    // 页面停留越久请求越多；新增的逐页结果读取也会被后续 tick 反复覆写。
    let stopped = false;
    let timer: number | undefined;
    const stopPolling = () => {
      stopped = true;
      if (timer !== undefined) {
        window.clearInterval(timer);
        timer = undefined;
      }
    };
    const total = slidesState.mode === 'real' ? slidesState.slides.length : 0;
    const refresh = async () => {
      try {
        const job = await getJob(identity, oneDraftJobId);
        if (cancelled) return;
        const pct = Math.max(0, Math.min(100, job.progressPercent || 0));
        const done = total > 0 ? Math.round((pct / 100) * total) : pct;
        if (job.state === 'JOB_STATE_SUCCEEDED') {
          stopPolling();
          setOneDraftRunning(false);
          setOneDraftProgress({ done: total || 100, total: total || 100, message: t('editor.oneDraftProgressComplete', { total: total || 100 }) });
          // 逐页结果：进度百分比只说明"跑完了"，跳过的页不会体现在百分比里。
          // 步骤计数是后端逐页登记的结果（success=生成 / skipped=跳过），失败时明确标注不可用。
          try {
            const detail = await getJobDetail(identity, oneDraftJobId);
            if (!cancelled) {
              if (detail.stepsError) {
                setOneDraftResult(null);
                setOneDraftResultUnavailable(true);
              } else {
                const counts = detail.stepCounts ?? {};
                setOneDraftResult({ generated: counts.success ?? 0, skipped: counts.skipped ?? 0 });
                setOneDraftResultUnavailable(false);
              }
            }
          } catch {
            if (!cancelled) {
              setOneDraftResult(null);
              setOneDraftResultUnavailable(true);
            }
          }
          if (slidesState.mode === 'real') {
            const found: Record<string, ScriptRevision> = {};
            await Promise.all(slidesState.slides.map(async (slide) => {
              try {
                found[slide.slideId] = await getScript(identity, projectId, slide.slideId);
              } catch {
                // 单页刷新失败不影响任务完成提示。
              }
            }));
            if (!cancelled && Object.keys(found).length > 0) setRealScripts((current) => ({ ...current, ...found }));
            if (!cancelled) setScriptsRefreshNonce((n) => n + 1);
          }
          return;
        }
        if (job.state === 'JOB_STATE_FAILED' || job.state === 'JOB_STATE_CANCELED') {
          stopPolling();
          setOneDraftRunning(false);
          setOneDraftProgress({ done, total: total || 100, message: friendlyOneDraftError(job.lastError?.message || '') });
          return;
        }
        setOneDraftRunning(true);
        setOneDraftProgress({ done, total: total || 100, message: t('editor.oneDraftProgressPercent', { percent: pct }) });
      } catch (error) {
        if (!cancelled) setOneDraftProgress((current) => ({ ...current, message: friendlyOneDraftError(error instanceof Error ? error.message : '') }));
      }
    };
    void refresh();
    if (!stopped) timer = window.setInterval(() => { if (!stopped) void refresh(); }, 2000);
    return () => {
      cancelled = true;
      stopPolling();
    };
  }, [identity, oneDraftJobId, projectId, slidesState, t]);

  // 提交通道：**按传入的 slideId** 提交（不再闭包 activeSlideID）——
  // 提交在途时用户可能已切页，原页在途期间的编辑仍须能落库（R-13，见 ScriptEditor 草案表）。
  const commitRealScript = useCallback(
    async (slideId: string, segments: ScriptSegment[], expectedRevision: number): Promise<ScriptRevision> => {
      const result = await updateScriptApi(identity, projectId, slideId, expectedRevision, segments);
      if (result.conflict && result.latest) {
        setConflict({
          slideId,
          localText: segments.map((segment) => segment.displayText).join('\n\n'),
          latest: result.latest
        });
        // 抛哨兵错误：编辑器据此**放弃**本地草案（本地文本已存进 conflict.localText，
        // 两条出口都在本组件的对照面板里），避免把用户明确放弃的文本自动写回去。
        throw new ScriptConflictError(t('editor.conflict.default'));
      }
      return result.revision;
    },
    [identity, projectId, t]
  );

  // B2 M2 ⑧：切换页面前先刷新（提交）当前页待保存队列，避免丢失未保存编辑。
  const handleSlideSelect = (slideId: string) => {
    if (slideId === activeSlideID) return;
    scriptEditorRef.current?.flush();
    setActiveSlideID(slideId);
  };

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

  // 讲稿被改动：标记"语音待更新"，供讲稿栏提示。
  const handleScriptEdited = useCallback(() => setVoiceDirty(true), []);

  // 语音属性弹窗：可用音色（来自所选模型的网关配置）。
  const voicesForModel = (name: string): string[] => voiceModels.find((m) => m.name === name)?.voices ?? [];
  const voiceDisplayName = (voice: string): string => {
    const raw = voice.trim();
    const suffix = raw.includes(':') ? raw.split(':').pop() || raw : raw;
    return suffix || raw;
  };

  const friendlyOneDraftError = (message: string): string => {
    if (/context deadline exceeded|Client\.Timeout|awaiting headers/i.test(message)) {
      return t('editor.oneDraftTimeout');
    }
    return message || t('editor.oneDraftProgressJobFailed');
  };

  // 读取"未配音/讲稿已更新"的页，供弹窗展示增量范围。
  const refreshVoiceStale = async () => {
    setVoiceStaleLoading(true);
    try {
      const res = await getNarrationStale(identity, projectId);
      setVoiceStaleIds(res.slides.filter((slide) => slide.stale).map((slide) => slide.slideId));
    } catch {
      setVoiceStaleIds([]);
    } finally {
      setVoiceStaleLoading(false);
    }
  };

  const openVoiceDialog = () => {
    setVoiceSaveError('');
    setVoiceDraftModel(voiceModelName);
    setVoiceDraftVoice(voiceId);
    setVoiceDraftRate(ratePercent);
    setVoiceGenMode('incremental');
    setPropsOpen(true);
    void refreshVoiceStale();
  };

  const selectVoiceModel = (name: string) => {
    setVoiceDraftModel(name);
    const voices = voicesForModel(name);
    setVoiceDraftVoice(voices[0] ?? '');
  };

  // 「配音」：先保存语音配置，再按所选生成方式发起配音任务。
  const voiceDialogGenerate = async () => {
    setVoiceSaving(true);
    setVoiceSaveError('');
    try {
      const saved = await saveVoiceSettings(identity, projectId, {
        model: voiceDraftModel,
        voice: voiceDraftVoice,
        ratePercent: voiceDraftRate
      });
      setVoiceModelName(saved.model);
      setRatePercent(saved.ratePercent || 100);
      if (saved.voice) setVoiceId(saved.voice);
      setPropsOpen(false);
      setVoiceSaving(false);
      // 生成在弹窗关闭后继续；进度显示在讲稿栏与顶部任务提示。
      void runVoiceGeneration(voiceGenMode, {
        voiceId: saved.voice || voiceDraftVoice,
        ratePercent: saved.ratePercent || voiceDraftRate
      });
    } catch (error) {
      setVoiceSaveError(error instanceof Error ? error.message : t('editor.voiceSaveFailed'));
      setVoiceSaving(false);
    }
  };

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

  // 拖拽预览区与讲稿栏之间的竖条，实时改写 --script-panel-width。
  const startScriptResize = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const layout = editorLayoutRef.current;
    if (!layout) return;
    event.preventDefault();
    const handle = event.currentTarget;
    const pointerId = event.pointerId;
    handle.setPointerCapture?.(pointerId);
    setScriptResizing(true);
    const rect = layout.getBoundingClientRect();
    const onMove = (moveEvent: PointerEvent) => {
      // 上限 = 容器宽 - 左栏 200 - 预览最小 300 - 三处间距 30，保证预览区不被压塌。
      const max = Math.max(280, rect.width - 530);
      const width = Math.min(Math.max(rect.right - moveEvent.clientX, 280), max);
      layout.style.setProperty('--script-panel-width', `${Math.round(width)}px`);
    };
    const onEnd = () => {
      setScriptResizing(false);
      handle.releasePointerCapture?.(pointerId);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onEnd);
      window.removeEventListener('pointercancel', onEnd);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onEnd);
    window.addEventListener('pointercancel', onEnd);
  };

  // 生成/重新生成讲稿：按模式对指定页生成，轮询直到 revision 变化（避免读到旧稿）。
  const generateScript = async (
    mode: ScriptMode,
    slideIds: string[],
    overwrite = false,
    options: { language?: string; sourceMode?: 'notes_first' | 'page_content' | 'notes_only' } = {}
  ) => {
    if (!isReady || draftStatus.phase === 'generating') return false;
    if (slideIds.length === 0) {
      setDraftStatus({ phase: 'error', message: t('editor.noScriptsInScope') });
      return false;
    }
    // 记录基线 revision：只有拿到不同的 revision 才认为新稿已落库。
    const baseline: Record<string, number> = {};
    const baselineText: Record<string, string> = {};
    for (const id of slideIds) {
      baseline[id] = Number(realScripts[id]?.revision ?? -1);
      baselineText[id] = (realScripts[id]?.segments ?? []).map((segment) => segment.displayText).join('\n');
    }

    setDraftStatus({ phase: 'generating', message: t('editor.generateQueued', { mode: modeLabel(mode, t) }) });
    try {
      // 统一走原生端点：它会绑定项目当前版本，并支持来源/受众/风格/长度与覆盖策略。
      await regenerateScriptDraft(identity, projectId, slideIds, mode, { ...options, overwrite });
      const targets = slidesState.mode === 'real' ? slidesState.slides.filter((slide) => slideIds.includes(slide.slideId)) : [];
      const found: Record<string, ScriptRevision> = {};
      const deadline = Date.now() + 120_000;
      while (Date.now() < deadline) {
        for (const slide of targets) {
          if (found[slide.slideId]) continue;
          try {
            const rev = await getScript(identity, projectId, slide.slideId);
            if (Number(rev.revision) !== baseline[slide.slideId]) found[slide.slideId] = rev;
          } catch {
            // 尚未生成完成，继续等待。
          }
        }
        if (Object.keys(found).length > 0) setRealScripts((current) => ({ ...current, ...found }));
        if (targets.every((slide) => found[slide.slideId])) break;
        await sleep(1500);
      }
      const ready = targets.length > 0 && targets.every((slide) => found[slide.slideId]);
      // 与生成前文本对比：内容未变时给出明确反馈（如原文朗读对已是原文的页）。
      const textOf = (rev: ScriptRevision) => rev.segments.map((segment) => segment.displayText).join('\n');
      const changed = targets.some(
        (slide) => found[slide.slideId] && textOf(found[slide.slideId]) !== (baselineText[slide.slideId] ?? '')
      );
      // 讲稿内容确实变化时才标记"语音待更新"，避免无变化的空操作也提示。
      if (changed) setVoiceDirty(true);
      setDraftStatus(
        ready
          ? {
              phase: 'ready',
              message: changed
                ? t('editor.generateDone', { count: targets.length })
                : t('editor.scriptUnchanged')
            }
          : { phase: 'error', message: t('editor.generateBackground') }
      );
      return ready;
    } catch (error) {
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.generateFailed') });
      return false;
    }
  };

  // 生成面板（语音抽屉）沿用所选模式，针对当前页。
  const generateAll = () => void generateScript(draftMode, [activeSlideID]);

  useEffect(() => {
    if (!canAutoCreateScriptFromNotes || !activeSlideID) return;
    if (autoNotesDraftRef.current.has(activeSlideID)) return;
    if (draftStatus.phase === 'generating' || oneDraftRunning) return;
    autoNotesDraftRef.current.add(activeSlideID);
    void generateScript('SCRIPT_MODE_ORIGINAL', [activeSlideID], false, { sourceMode: 'notes_only' }).then((ok) => {
      if (!ok) setAutoNotesFailed((prev) => (prev.includes(activeSlideID) ? prev : [...prev, activeSlideID]));
    });
  }, [activeSlideID, canAutoCreateScriptFromNotes, draftStatus.phase, oneDraftRunning]);

  const submitOneDraft = async () => {
    if (slidesState.mode !== 'real' || oneDraftRunning) return;
    const slideIds = slidesState.slides.map((slide) => slide.slideId);
    const targetSeconds = oneDraftLength === 'brief' ? 30 : oneDraftLength === 'detailed' ? 90 : 60;
    // 生成语言即讲稿展示语言：先切换偏好，面板随后按该语言加载生成结果。
    setScriptLanguagePreference(oneDraftLanguage);
    persistScriptLanguage(oneDraftLanguage);
    setOneDraftRunning(true);
    setOneDraftResult(null);
    setOneDraftResultUnavailable(false);
    setOneDraftProgress({ done: 0, total: slideIds.length, message: t('editor.oneDraftProgressStart') });
    try {
      const result = await regenerateScriptDraft(identity, projectId, slideIds, oneDraftMode, {
        language: oneDraftLanguage,
        sourceMode: oneDraftSource,
        audience: oneDraftAudience,
        style: oneDraftStyle,
        targetSeconds,
        overwrite: oneDraftOverwrite === 'overwrite_all'
      });
      window.localStorage.setItem(oneDraftStorageKey, result.jobId);
      setOneDraftJobId(result.jobId);
      setOneDraftProgress({ done: 0, total: slideIds.length, message: t('editor.oneDraftProgressQueued') });
    } catch (error) {
      setOneDraftRunning(false);
      setOneDraftProgress({ done: 0, total: slideIds.length, message: friendlyOneDraftError(error instanceof Error ? error.message : t('editor.generateFailed')) });
    }
  };

  // 原文朗读：直接用当前页 PPT 备注栏文本覆盖讲稿，避免后台从版面文字误取源。
  const copyNotesToScriptActive = async () => {
    if (!activeSlideID || !activeRealScript) return;
    const notes = slideNotesText.trim();
    if (!notes) {
      setDraftStatus({ phase: 'error', message: t('editor.originalNeedsNotes') });
      return;
    }
    setDraftStatus({ phase: 'generating', message: t('editor.copyingOriginalNotes') });
    try {
      const current = await getScript(identity, projectId, activeSlideID).catch(() => activeRealScript);
      const base = current.segments[0];
      const segment: ScriptSegment = {
        segmentId: base?.segmentId || 'seg-01',
        slideId: activeSlideID,
        displayText: notes,
        spokenText: notes,
        sourceRefs: [activeSlideID],
        sourceAnchors: [{ slideId: activeSlideID, shapeId: '', kind: 'notes', raw: notes, confidence: 1 }],
        status: 'draft'
      };
      const result = await updateScriptApi(identity, projectId, activeSlideID, current.revision, [segment]);
      if (result.conflict && result.latest) {
        setRealScripts((current) => ({ ...current, [activeSlideID]: result.latest! }));
        setDraftStatus({ phase: 'error', message: t('script.conflict') });
        return;
      }
      setRealScripts((current) => ({ ...current, [activeSlideID]: result.revision }));
      setVoiceDirty(true);
      setDraftStatus({ phase: 'ready', message: t('editor.originalCopied') });
    } catch (error) {
      setDraftStatus({ phase: 'error', message: error instanceof Error ? error.message : t('editor.generateFailed') });
    }
  };

  // 讲稿栏「重新生成讲稿」：先落库本页未保存编辑，再按所选模式重新生成当前页。
  const regenerateScriptActive = async (mode: ScriptMode) => {
    if (!activeSlideID) return;
    scriptEditorRef.current?.flush();
    const waitDeadline = Date.now() + 5000;
    while (scriptEditorRef.current?.isDirty() && Date.now() < waitDeadline) {
      await sleep(200);
    }
    if (mode === 'SCRIPT_MODE_ORIGINAL') {
      await copyNotesToScriptActive();
      return;
    }
    await generateScript(mode, [activeSlideID], true);
  };

  // 全部已有讲稿的页（配音时间轴须完整覆盖，否则未提交的页会从播放器消失）。
  const allScriptSlideIds = (): string[] => {
    // 闭包内 TS 不会沿用 isReady 的别名收窄，需就地判别可辨识联合。
    if (slidesState.mode !== 'real') return activeRealScript ? [activeSlideID] : [];
    const ids = slidesState.slides.filter((slide) => realScripts[slide.slideId]).map((slide) => slide.slideId);
    if (ids.length === 0 && activeRealScript) return [activeSlideID];
    return ids;
  };

  // 核心配音流程：创建生成任务 → 轮询 narration → 构建播放清单。
  // 讲稿随时可编辑、自动保存，不再要求"确认/锁定"（lockConfirmedOnly=false，后端 narration.go:92/110
  // 仅在 RequireConfirmed 时才校验状态）。
  const generateNarrationFor = async (
    slideIds: string[],
    override: { voiceId?: string; ratePercent?: number } = {}
  ) => {
    if (!isReady || narrationStatus.phase === 'generating') return;
    if (slideIds.length === 0) {
      setNarrationStatus({ phase: 'error', message: t('editor.noScriptsInScope') });
      return;
    }
    const selectedVoice = override.voiceId || voiceId || devNarrationVoiceID;
    const selectedRate = override.ratePercent ?? ratePercent;
    setNarrationStatus({ phase: 'generating', message: t('editor.narrationQueued') });
    try {
      try {
        const est = await estimateNarration(identity, projectId, slideIds, selectedVoice, selectedRate);
        setNarrationEstimate(est);
        setNarrationStatus({
          phase: 'generating',
          message: t('editor.narrationEstimate', { minutes: Math.round(est.estimatedSeconds / 60) })
        });
      } catch {
        // 预估失败不阻塞生成。
      }
      const previousTimelineKey = realManifest?.timelineKey ?? '';
      const idempotencyKey = `narration-${projectId}-${Date.now()}`;
      const generation = await createGeneration(identity, projectId, slideIds, selectedVoice, idempotencyKey, {
        ratePercent: selectedRate,
        lockConfirmedOnly: false
      });
      // B3-M5：生成任务已创建，立即刷新活跃任务，让顶部快照提示尽快出现。
      void refreshActiveGenJobs();
      const deadline = Date.now() + 120_000;
      let status;
      let jobSucceeded = false;
      while (Date.now() < deadline) {
        await sleep(1500);
        try {
          const job = await getJob(identity, generation.jobId);
          if (job.state === 'JOB_STATE_FAILED' || job.state === 'JOB_STATE_CANCELED') {
            setNarrationStatus({ phase: 'error', message: job.lastError?.message || t('editor.narrationFailed') });
            return;
          }
          if (job.state === 'JOB_STATE_SUCCEEDED') jobSucceeded = true;
        } catch {
          // 任务查询失败不阻塞，继续用 narration 状态兜底。
        }
        status = await getNarration(identity, projectId);
        if (status.ready && (jobSucceeded || !previousTimelineKey || status.timelineKey !== previousTimelineKey)) break;
      }
      if (!status || !status.ready || (!jobSucceeded && previousTimelineKey && status.timelineKey === previousTimelineKey)) {
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
      if (slidesState.mode === 'real' && !manifestMatchesSlides(manifest, slidesState.slides)) {
        setRealManifest(null);
        setNarrationStatus({ phase: 'error', message: t('editor.narrationVersionMismatch') });
        return;
      }
      setRealManifest(manifest);
      setVoiceDirty(false);
      void refreshVoiceStale();
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

  // 配音生成：后端每次都会重建整条时间轴，因此两种模式都提交"全部有讲稿的页"以保留其他页。
  // 区别在语义：增量只在存在"未配音/讲稿已更新"的页时才发起（其余段落按内容哈希命中缓存）；
  // 全部始终发起，配合新音色/语速会重新合成全部段落并覆盖旧音频。
  const runVoiceGeneration = async (
    mode: 'incremental' | 'full',
    override: { voiceId?: string; ratePercent?: number } = {}
  ) => {
    scriptEditorRef.current?.flush();
    const waitDeadline = Date.now() + 5000;
    while (scriptEditorRef.current?.isDirty() && Date.now() < waitDeadline) {
      await sleep(200);
    }
    const ids = allScriptSlideIds();
    if (ids.length === 0) {
      setNarrationStatus({ phase: 'error', message: t('editor.noScriptsInScope') });
      return;
    }
    if (mode === 'incremental') {
      let stale = voiceStaleIds;
      try {
        const res = await getNarrationStale(identity, projectId);
        stale = res.slides.filter((slide) => slide.stale).map((slide) => slide.slideId);
        setVoiceStaleIds(stale);
      } catch {
        // 统计失败时按"有待生成页"处理，避免静默跳过用户的配音请求。
      }
      if (stale.length === 0) {
        setNarrationStatus({ phase: 'ready', message: t('editor.voiceAllFresh') });
        return;
      }
    }
    await generateNarrationFor(ids, override);
  };

  // 讲稿栏「重新生成语音」：先落库本页未保存编辑，再生成。
  const regenerateVoiceActive = async () => {
    if (!activeRealScript) return;
    scriptEditorRef.current?.flush();
    const deadline = Date.now() + 5000;
    while (scriptEditorRef.current?.isDirty() && Date.now() < deadline) {
      await sleep(200);
    }
    await generateNarrationFor(allScriptSlideIds());
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
          <div className="editor-title-row">
            <span className="editor-title" title={projectTitle || projectId}>{projectTitle || projectId}</span>
            {revisions.length > 0 && (
              <>
                <span className="editor-title-sep">·</span>
                <select
                  className="version-select"
                  value={initRevisionNo ?? currentRevision}
                  onChange={(e) => {
                    const v = Number(e.currentTarget.value);
                    if (v === currentRevision) return;
                    navigate(`/projects/${projectId}/editor${v === currentRevision ? '' : `?rev=${v}`}`);
                  }}
                  title={t('versions.openTitle')}
                >
                  {revisions.map((rv) => (
                    <option key={rv.revisionNo} value={rv.revisionNo} disabled={rv.isCurrent}>
                      v{rv.revisionNo} · {rv.displayName}
                      {rv.isCurrent ? ` (${t('versions.current')})` : ''}
                    </option>
                  ))}
                </select>
              </>
            )}
          </div>
        </div>
        <div className="editor-header-right">
          <span className={`status-marker ${unsaved ? 'unsaved' : ''} ${isReady ? '' : 'muted'}`}>
            {unsaved ? t('editor.unsaved') : statusMarker}
          </span>
          <button type="button" className="button-ghost" onClick={() => setOneDraftOpen(true)} disabled={!isReady || draftStatus.phase === 'generating'}>
            {t('editor.oneDraft')}
          </button>
          <button type="button" className="button-ghost" onClick={openVoiceDialog} title={t('editor.propertiesTitle')}>
            {t('editor.voiceButton')}
          </button>
          {/* B4-M1：导出要求 EDITOR（export.go:39）；无配音快照时无处可导，不渲染。 */}
          {canExport && realManifest && (
            <button
              type="button"
              className="button-ghost"
              disabled={exporting}
              onClick={() => {
                setExportError('');
                setExportOpen(true);
              }}
            >
              {t('editor.export')}
            </button>
          )}
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

      <div className={`editor-layout${scriptOpen ? '' : ' script-collapsed'}${scriptResizing ? ' script-resizing' : ''}`} ref={editorLayoutRef}>
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
          {realManifest ? (
            <Player
              manifest={realManifest}
              activeSlideId={activeSlideID || undefined}
              activeImageUrl={activeRenderURL}
              activeImageLabel={activeSlide?.title}
              embedded
              onSlideChange={handleSlideSelect}
            />
          ) : activeRenderURL ? (
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
          {/* 幻灯片备注编辑器：位于 PPT 显示区下方（播放器预览上方） */}
          {slidesState.mode === 'real' && activeSlideID && (
            <section className="notes-editor" aria-label={t('editor.notesAria')}>
              <header>
                <span className="eyebrow">{t('editor.notesEyebrow')}</span>
                {notesSaving && <span className="notes-saving">{t('common.saving')}</span>}
              </header>
              <textarea
                className="notes-textarea"
                value={slideNotesText}
                onChange={(e) => setSlideNotesText(e.currentTarget.value)}
                onBlur={() => scheduleSaveNotes(slideNotesText, activeSlideID, initRevisionNo ?? currentRevision)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                    e.preventDefault();
                    scheduleSaveNotes(slideNotesText, activeSlideID, initRevisionNo ?? currentRevision);
                  }
                }}
                placeholder={t('editor.notesPlaceholder')}
                rows={3}
              />
              {notesError && <p className="form-error">{notesError}</p>}
            </section>
          )}
        </section>

        {scriptOpen && (
          <button
            type="button"
            className="script-resize-handle"
            onPointerDown={startScriptResize}
            title={t('editor.scriptResize')}
            aria-label={t('editor.scriptResize')}
          />
        )}

        {scriptOpen && (
        <section className="script-panel">
          <div className="script-panel-head">
            <button
              type="button"
              className="script-toggle"
              onClick={() => setScriptOpen(false)}
              title={t('editor.scriptCollapse')}
              aria-label={t('editor.scriptCollapse')}
              aria-expanded={scriptOpen}
            >
              ›
            </button>
            <span className="eyebrow">{t('editor.scriptEyebrow')}</span>
            <label className="script-language" title={t('editor.scriptLanguageTitle')}>
              <select
                value={scriptLanguage || 'zh-CN'}
                onChange={(e) => persistScriptLanguage(e.currentTarget.value)}
                aria-label={t('editor.scriptLanguage')}
              >
                <option value="zh-CN">{t('editor.languageChinese')}</option>
                <option value="en-US">{t('editor.languageEnglish')}</option>
              </select>
            </label>
          </div>
          <div className="script-column-body">
            {scriptsLoadError && (
              <div className="load-failure" role="alert">
                <p className="form-error">{scriptsLoadError}</p>
                <button type="button" onClick={() => setScriptsRefreshNonce((nonce) => nonce + 1)}>{t('common.retry')}</button>
              </div>
            )}
            {activeRealScript ? (
            <ScriptEditor
              ref={scriptEditorRef}
              script={activeRealScript}
              slideTitle={activeSlide?.title || activeSlideID}
              onChange={(next) => setRealScripts((current) => ({ ...current, [next.slideId]: next }))}
              commit={commitRealScript}
              onCommitError={commitError}
              onStatusChange={handleScriptStatus}
              canEdit={canEditScript}
              onEdited={handleScriptEdited}
              onRegenerateScript={canEditScript ? regenerateScriptActive : undefined}
              scriptBusy={draftStatus.phase === 'generating'}
              scriptStatusText={draftStatus.message}
              scriptError={draftStatus.phase === 'error'}
              onRegenerateVoice={canGenerate ? regenerateVoiceActive : undefined}
              voiceBusy={voiceBusy}
              voiceProgress={voiceProgress}
              voiceStatusText={voiceStatusText}
              voiceNeedsUpdate={voiceDirty}
              regeneratingIds={regeneratingIds}
              onRegenerate={regenerateActive}
              onAddToDictionary={addToDictionary}
            />
          ) : (
            <section className="editor-card">
              <header>
                <div>
                  <span className="eyebrow">{t('editor.scriptEyebrow')}</span>
                  <h2>{activeSlide?.title || activeSlideID || t('editor.scriptEyebrow')}</h2>
                </div>
              </header>
              {isReady ? (
                <>
                  <p className="empty-state">
                    {canAutoCreateScriptFromNotes ? t('editor.creatingScriptFromNotes') : draftStatus.message || t('editor.draftHint')}
                  </p>
                  {!canAutoCreateScriptFromNotes && (
                    <div className="draft-options" aria-label={t('editor.scriptMode')}>
                      {scriptModeOptions.map((option) => (
                        <label key={option.value} className={draftMode === option.value ? 'selected' : ''}>
                          <input type="radio" name="draft-mode" value={option.value} checked={draftMode === option.value} onChange={() => setDraftMode(option.value)} />
                          <span>{t(option.labelKey)}</span>
                          <small>{t(option.descKey)}</small>
                        </label>
                      ))}
                    </div>
                  )}

                  {/* M3 ⑥：无备注页显式选择讲稿来源（仅当前页，随左侧页面切换；持久化后由 Worker 尊重）。 */}
                  {activeSlide && !activeSlide.hasNotes && (
                    <div className="source-selector" aria-label={t('editor.noNotesSource')}>
                      <span className="eyebrow">{t('editor.chooseSourceForPage')}</span>
                      <div className="source-row">
                        <strong className="nowrap-ellipsis" title={activeSlide.slideId}>
                          {activeSlide.title || activeSlide.slideId}
                        </strong>
                        <select
                          value={activeSource}
                          onChange={(e) => {
                            const value = e.currentTarget.value;
                            if (value === 'custom') {
                              // 自定义来源需先有文本再持久化（后端校验 customText 非空）；
                              // 此处仅本地切换为 custom 以展示输入框，输入后由下方 onChange 持久化。
                              setSlideSources((current) => ({
                                ...current,
                                [activeSlide.slideId]: { slideId: activeSlide.slideId, source: 'custom', customText: activeCustom }
                              }));
                            } else {
                              saveSlideSource(activeSlide.slideId, value);
                            }
                          }}
                        >
                          <option value="layout">{t('editor.sourceLayout')}</option>
                          <option value="title">{t('editor.sourceTitle')}</option>
                          <option value="body">{t('editor.sourceBody')}</option>
                          <option value="notes">{t('editor.sourceNotes')}</option>
                          <option value="custom">{t('editor.sourceCustom')}</option>
                        </select>
                        {activeSource === 'custom' && (
                          <input
                            className="source-custom"
                            placeholder={t('editor.customSourcePlaceholder')}
                            value={activeCustom}
                            onChange={(e) => {
                              const text = e.currentTarget.value;
                              if (text.trim() !== '') saveSlideSource(activeSlide.slideId, 'custom', text);
                              else setSlideSources((current) => ({ ...current, [activeSlide.slideId]: { slideId: activeSlide.slideId, source: 'custom', customText: '' } }));
                            }}
                          />
                        )}
                      </div>
                    </div>
                  )}
                  {!canAutoCreateScriptFromNotes && (
                    <div className="draft-actions">
                      <button type="button" disabled={draftStatus.phase === 'generating'} onClick={() => void generateAll()}>
                        {draftStatus.phase === 'generating'
                          ? t('editor.generating')
                          : draftRequested
                            ? t('editor.startGenerate')
                            : t('editor.generateMode', { mode: modeLabel(draftMode, t) })}
                      </button>
                    </div>
                  )}
                </>
              ) : (
                <p className="empty-state">{t('editor.notParsed')}</p>
              )}
            </section>
          )}
          </div>
        </section>
        )}
      </div>

      {!scriptOpen && (
        <button
          type="button"
          className="script-reopen"
          onClick={() => setScriptOpen(true)}
          title={t('editor.scriptExpand')}
          aria-label={t('editor.scriptExpand')}
        >
          <span className="script-reopen-glyph">‹</span>
          <span className="script-reopen-label">{t('editor.scriptEyebrow')}</span>
        </button>
      )}

      {/* 语音属性抽屉：仅语音模型 / 音色 / 语速 + 保存。保存后持久化到数据库。 */}
      {propsOpen && (
        <div className="drawer-backdrop" onClick={() => setPropsOpen(false)}>
          <aside className="properties-drawer" role="dialog" aria-modal="true" aria-label={t('editor.properties')} onClick={(event) => event.stopPropagation()} ref={propsDialogRef}>
            <header>
              <div>
                <span className="eyebrow">{t('editor.properties')}</span>
                <h2>{t('editor.properties')}</h2>
              </div>
              <button type="button" className="dialog-close-icon" onClick={() => setPropsOpen(false)} aria-label={t('common.close')} title={t('common.close')}>
                ×
              </button>
            </header>
            <section className="panel nested">
              <label className="field-label">
                {t('editor.voiceModel')}
                <select
                  value={voiceDraftModel}
                  onChange={(e) => selectVoiceModel(e.currentTarget.value)}
                  disabled={voiceModels.length === 0}
                >
                  {voiceModels.length === 0 && <option value="">{t('editor.voiceModelEmpty')}</option>}
                  {voiceModels.map((model) => (
                    <option key={model.name} value={model.name}>
                      {model.model || model.name}
                      {model.isDefault ? ` · ${t('editor.voiceModelDefault')}` : ''}
                    </option>
                  ))}
                </select>
              </label>

              <label className="field-label">
                {t('editor.voice')}
                <select
                  value={voiceDraftVoice}
                  onChange={(e) => setVoiceDraftVoice(e.currentTarget.value)}
                  disabled={voicesForModel(voiceDraftModel).length === 0}
                >
                  {(voicesForModel(voiceDraftModel).length > 0
                    ? voicesForModel(voiceDraftModel)
                    : [voiceDraftVoice || devNarrationVoiceID]
                  ).map((voice) => (
                    <option key={voice} value={voice}>
                      {voiceDisplayName(voice)}
                    </option>
                  ))}
                </select>
              </label>

              <label className="field-label">
                {t('editor.rate', { percent: voiceDraftRate })}
                <select value={voiceDraftRate} onChange={(e) => setVoiceDraftRate(Number(e.currentTarget.value))}>
                  {[75, 90, 100, 110, 125, 150].map((rate) => (
                    <option key={rate} value={rate}>
                      {rate === 100 ? t('editor.rateStandard') : `${rate}%`}
                    </option>
                  ))}
                </select>
              </label>

              <label className="field-label">
                {t('editor.voiceGenerateMode')}
                <select value={voiceGenMode} onChange={(e) => setVoiceGenMode(e.currentTarget.value as 'incremental' | 'full')}>
                  <option value="incremental">{t('editor.voiceModeIncremental')}</option>
                  <option value="full">{t('editor.voiceModeFull')}</option>
                </select>
              </label>

              <p className="narration-note">
                {voiceStaleLoading
                  ? t('editor.voiceStaleLoading')
                  : voiceGenMode === 'full'
                    ? t('editor.voiceFullHint', { total: scriptReadyCount })
                    : t('editor.voiceStaleHint', { stale: voiceStaleIds.length, total: scriptReadyCount })}
              </p>

              {voiceModels.length === 0 && <p className="narration-note">{t('editor.voiceModelEmpty')}</p>}
              {voiceModels.length > 0 && <p className="narration-note">{t('editor.voiceModelNote')}</p>}
              {voiceSaveError && <p className="form-error">{voiceSaveError}</p>}

              <div className="draft-actions">
                <button type="button" className="primary" disabled={voiceSaving || !canEditScript} onClick={() => void voiceDialogGenerate()}>
                  {voiceSaving ? t('editor.saving') : t('editor.voiceSubmit')}
                </button>
                <button type="button" className="button-ghost" onClick={() => setPropsOpen(false)}>
                  {t('common.cancel')}
                </button>
              </div>
              {!canEditScript && <p className="perm-hint">{t('perm.needEditorExport')}</p>}
            </section>
          </aside>
        </div>
      )}

      {oneDraftOpen && (
        <div className="drawer-backdrop" onClick={() => setOneDraftOpen(false)}>
          <aside className="properties-drawer" role="dialog" aria-modal="true" aria-label={t('editor.oneDraft')} onClick={(event) => event.stopPropagation()} ref={oneDraftDialogRef}>
            <header>
              <div>
                <span className="eyebrow">{t('editor.oneDraftEyebrow')}</span>
                <h2>{t('editor.oneDraft')}</h2>
              </div>
              <button type="button" className="dialog-close-icon" onClick={() => setOneDraftOpen(false)} aria-label={t('common.close')} title={t('common.close')}>
                ×
              </button>
            </header>
            <section className="panel nested">
              <p className="narration-note">{t('editor.oneDraftNote')}</p>
              <label className="field-label">
                {t('editor.oneDraftLanguage')}
                <select value={oneDraftLanguage} disabled={oneDraftRunning} onChange={(e) => setOneDraftLanguage(e.currentTarget.value as 'zh-CN' | 'en-US')}>
                  <option value="zh-CN">{t('editor.languageChinese')}</option>
                  <option value="en-US">{t('editor.languageEnglish')}</option>
                </select>
              </label>
              <label className="field-label">
                {t('editor.oneDraftSource')}
                <select value={oneDraftSource} disabled={oneDraftRunning} onChange={(e) => setOneDraftSource(e.currentTarget.value as 'notes_first' | 'page_content' | 'notes_only')}>
                  <option value="notes_first">{t('editor.sourceNotesFirst')}</option>
                  <option value="notes_only">{t('editor.sourceNotesOnly')}</option>
                  <option value="page_content">{t('editor.sourcePageContent')}</option>
                </select>
              </label>
              <label className="field-label">
                {t('editor.oneDraftMode')}
                <select value={oneDraftMode} disabled={oneDraftRunning} onChange={(e) => setOneDraftMode(e.currentTarget.value as ScriptMode)}>
                  <option value="SCRIPT_MODE_ORIGINAL">{t('editor.mode.original')}</option>
                  <option value="SCRIPT_MODE_POLISH">{t('editor.mode.polish')}</option>
                  <option value="SCRIPT_MODE_AI_GENERATED">{t('editor.mode.ai')}</option>
                </select>
              </label>
              <label className="field-label">
                {t('editor.oneDraftLength')}
                <select value={oneDraftLength} disabled={oneDraftRunning} onChange={(e) => setOneDraftLength(e.currentTarget.value as 'brief' | 'standard' | 'detailed')}>
                  <option value="brief">{t('editor.lengthBrief')}</option>
                  <option value="standard">{t('editor.lengthStandard')}</option>
                  <option value="detailed">{t('editor.lengthDetailed')}</option>
                </select>
              </label>
              <label className="field-label">
                {t('editor.oneDraftStyle')}
                <select value={oneDraftStyle} disabled={oneDraftRunning} onChange={(e) => setOneDraftStyle(e.currentTarget.value)}>
                  <option value="专业正式">{t('editor.styleFormal')}</option>
                  <option value="销售演示">{t('editor.styleSales')}</option>
                  <option value="培训讲解">{t('editor.styleTraining')}</option>
                  <option value="口语自然">{t('editor.styleCasual')}</option>
                </select>
              </label>
              <label className="field-label">
                {t('editor.oneDraftAudience')}
                <select value={oneDraftAudience} disabled={oneDraftRunning} onChange={(e) => setOneDraftAudience(e.currentTarget.value)}>
                  <option value="通用听众">{t('editor.audienceGeneral')}</option>
                  <option value="客户/甲方">{t('editor.audienceClient')}</option>
                  <option value="内部汇报">{t('editor.audienceInternal')}</option>
                  <option value="技术团队">{t('editor.audienceTechnical')}</option>
                  <option value="管理层/投资人">{t('editor.audienceExecutive')}</option>
                </select>
              </label>
              <label className="field-label">
                {t('editor.oneDraftOverwrite')}
                <select value={oneDraftOverwrite} disabled={oneDraftRunning} onChange={(e) => setOneDraftOverwrite(e.currentTarget.value as 'fill_empty' | 'overwrite_all')}>
                  <option value="fill_empty">{t('editor.overwriteFillEmpty')}</option>
                  <option value="overwrite_all">{t('editor.overwriteAll')}</option>
                </select>
              </label>
              <p className="narration-note">{t('editor.oneDraftGuardrails')}</p>
              {oneDraftPlan && (
                <p className="narration-note">
                  {t('editor.oneDraftPlan', { generate: oneDraftPlan.generate, skip: oneDraftPlan.skippedExisting })}
                  {oneDraftPlan.reliable ? '' : ` ${t('editor.oneDraftPlanEstimate')}`}
                </p>
              )}
              {oneDraftPlan && oneDraftPlan.noText > 0 && (
                <p className="narration-note warn">{t('editor.oneDraftPlanNoText', { count: oneDraftPlan.noText })}</p>
              )}
              {oneDraftSource === 'page_content' && oneDraftMode === 'SCRIPT_MODE_ORIGINAL' && (
                <p className="narration-note warn">{t('editor.oneDraftPageContentOriginalHint')}</p>
              )}
              {oneDraftOverwrite === 'fill_empty' && (oneDraftPlan?.withNotes ?? 0) > 0 && (
                <p className="narration-note warn">{t('editor.oneDraftAutoNotesNote')}</p>
              )}
              <div className="draft-actions">
                <button type="button" className="primary" disabled={!canEditScript || oneDraftRunning || draftStatus.phase === 'generating'} onClick={() => void submitOneDraft()}>
                  {oneDraftRunning ? t('editor.scriptRegenerating') : t('editor.oneDraftSubmit')}
                </button>
                <button type="button" className="button-ghost" onClick={() => setOneDraftOpen(false)}>
                  {t('common.cancel')}
                </button>
              </div>
              {oneDraftProgress.total > 0 && (
                <div className="one-draft-progress" role="status" aria-live="polite">
                  <div className="one-draft-progress-bar" aria-hidden="true">
                    <span style={{ width: `${Math.round((oneDraftProgress.done / Math.max(1, oneDraftProgress.total)) * 100)}%` }} />
                  </div>
                  <span>{oneDraftProgress.message}</span>
                </div>
              )}
              {oneDraftResult && (
                <p className="narration-note" role="status">
                  {t('editor.oneDraftResult', { generated: oneDraftResult.generated, skipped: oneDraftResult.skipped })}
                </p>
              )}
              {!oneDraftResult && oneDraftResultUnavailable && (
                <p className="narration-note warn" role="status">{t('editor.oneDraftResultUnknown')}</p>
              )}
              {!canEditScript && <p className="perm-hint">{t('perm.needEditorExport')}</p>}
            </section>
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
    </div>
  );
}
