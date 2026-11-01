import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  AnimationConflictError,
  getAnimationAnchors,
  getAnimationSpec,
  getDefaultAnimationSpec,
  previewAnimation,
  saveAnimationSpec,
  type AnimationScope,
  type ClientIdentity
} from './api';
import { ConnectError } from './core';
import {
  TRIGGER_ENTER,
  anchorLabel,
  animationRuleKey,
  draftOf,
  editorStateOf,
  isDirty,
  savePayloadOf,
  segmentOfTrigger,
  segmentTrigger,
  summarize,
  withIntensity,
  withTrackTrigger,
  type AnimationAnchors,
  type AnimationPrefill,
  type AnimationSpec
} from './animationState';
import {
  carryBindings,
  filterBySource,
  prefillBindings,
  prefillLowConfidenceKey,
  prefillSignalKey,
  prefillSourceKey,
  reviewOrder,
  revokeBinding,
  revokeSource,
  sourceGroups
} from './animationPrefill';
import { useDialogA11y } from './a11y';
import { useI18n } from './i18n';
import { AnimationTrackTimeline } from './AnimationTrackTimeline';
import { emptyHistory, recordHistory, redoHistory, undoHistory, type TrackHistory } from './animationTrack';

// 三级入口的第一级（tasks 3.2）：
//
//   风格（预设） → 强度（只放大位移/缩放，不改时长） → 逐条要点的出现时机
//
// 刻意不做的东西：逐条 clip 的动效类型/时长编辑。那是第二、三级，
// 需要的是时间轴可视化而不是一个下拉框；先把"整页风格 + 哪条要点什么时候出来"做对，
// 用户就已经能得到一支能用的片子。
//
// 数据流：anchors（页上下文）与 spec（已保存规格）分开取——没配过动效的页也有锚点，
// 面板据此显示"选个风格开始"，而不是空白。
export function AnimationEditor({
  identity,
  projectId,
  slideId,
  slideTitle,
  sourceRevisionNo,
  language,
  timelineKey,
  canEdit,
  trackEditor,
  onClose
}: {
  identity: ClientIdentity;
  projectId: string;
  slideId: string;
  slideTitle?: string;
  sourceRevisionNo?: number;
  language: string;
  /** 预览用：时间轴键（页时长与分段偏移都来自它）。空 = 还没配音。 */
  timelineKey?: string;
  canEdit: boolean;
  /** 元素级轨道编辑是否开通（capabilities.trackEditor）。未开通时只显示一句说明。 */
  trackEditor: boolean;
  onClose: () => void;
}) {
  const { t } = useI18n();
  // 与其它抽屉同一套无障碍行为：Esc 关闭、焦点收在抽屉内。
  const dialogRef = useDialogA11y<HTMLElement>(onClose);
  const scope = useMemo<AnimationScope>(
    () => ({ projectId, slideId, sourceRevisionNo, language }),
    [projectId, slideId, sourceRevisionNo, language]
  );

  const [anchors, setAnchors] = useState<AnimationAnchors | null>(null);
  const [saved, setSaved] = useState<AnimationSpec | null>(null);
  const [draft, setDraft] = useState<AnimationSpec | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [conflict, setConflict] = useState<AnimationSpec | null>(null);
  // 每次成功保存 +1：顶上那句"已保存"要能让人看见，而不是靠猜按钮有没有灰。
  const [savedAt, setSavedAt] = useState(0);
  const [previewURL, setPreviewURL] = useState('');
  const [previewing, setPreviewing] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const [previewLayers, setPreviewLayers] = useState(0);
  // 预设生成请求在途：连点两个预设会发出两个默认规格请求，后到的那个赢——
  // 结果是用户点了 A 却拿到 B。用序号丢弃过期响应。
  const generateSeq = useRef(0);
  // 轨道编辑的撤销栈（只覆盖拖动/微调，不覆盖换预设与强度——那两件事用户自己能改回来）。
  const [history, setHistory] = useState<TrackHistory>(emptyHistory);
  // 这次会话里预填做了什么。规格里不留（保存后服务端不会回传 prefill），
  // 所以面板必须自己记着，否则「低置信」这条最重要的提示保存一次就消失了。
  const [prefillInfo, setPrefillInfo] = useState<AnimationPrefill | null>(null);
  // 生成那一刻「产出了几条轨道」的快照。不能现场数：撤销之后现场数会归零，
  // 于是「绑定 53 条，0 条产出轨道」这种假话会出现在用户刚撤完的那一刻。
  const [prefillTracks, setPrefillTracks] = useState<number | null>(null);
  const [prefillFilter, setPrefillFilter] = useState('');
  const [prefilling, setPrefilling] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    // 换页时把上一页的结果全部清掉：留着预览视频最糟——用户会拿上一页的画面
    // 判断这一页的动效，而两者根本没有关系。
    setConflict(null);
    setSaveError('');
    setPreviewError('');
    setSavedAt(0);
    setPreviewLayers(0);
    setPrefillInfo(null);
    setPrefillTracks(null);
    setPrefillFilter('');
    setPreviewURL((prev) => {
      if (prev) URL.revokeObjectURL(prev);
      return '';
    });
    try {
      const [nextAnchors, nextSpec] = await Promise.all([
        getAnimationAnchors(identity, scope),
        getAnimationSpec(identity, scope)
      ]);
      setAnchors(nextAnchors);
      setSaved(nextSpec);
      setDraft(nextSpec ? draftOf(nextSpec) : null);
    } catch (err) {
      setLoadError(errorText(err, t));
    } finally {
      setLoading(false);
    }
  }, [identity, scope, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // blob URL 必须显式释放：一次编辑会话可能预览十几次，不释放就一直占着内存。
  useEffect(() => {
    return () => {
      if (previewURL) URL.revokeObjectURL(previewURL);
    };
  }, [previewURL]);

  const state = editorStateOf(saved, canEdit);
  const dirty = isDirty(draft, saved);
  const summary = summarize(draft, anchors);

  // 面板内所有可编辑控件共用一条禁用判据：锁定 / 无权限 / 正在保存。
  const disabled = state.editDisabled || saving;

  // 一次编辑开始前压栈。拖拽只在 pointerdown 调一次——按像素记步会让撤销变成"一次撤 1ms"。
  const beginEdit = useCallback(() => {
    if (!draft || disabled) return;
    setHistory((current) => recordHistory(current, draft));
  }, [draft, disabled]);

  const undo = useCallback(() => {
    if (!draft || disabled) return;
    const result = undoHistory(history, draft);
    if (!result) return;
    setHistory(result.history);
    // 保留当前并发版本号：栈里的快照带的是"当时"的 revision，
    // 直接还原会让下一次保存发出一个过期的 expectedRevision，变成必然 409。
    setDraft({ ...result.spec, revision: draft.revision });
  }, [draft, disabled, history]);

  const redo = useCallback(() => {
    if (!draft || disabled) return;
    const result = redoHistory(history, draft);
    if (!result) return;
    setHistory(result.history);
    setDraft({ ...result.spec, revision: draft.revision });
  }, [draft, disabled, history]);

  // Ctrl/Cmd+Z / Shift+Ctrl/Cmd+Z。只在开通轨道编辑时接管：没这个能力时
  // 拦截撤销键会让人以为浏览器/输入框的撤销坏了。
  useEffect(() => {
    if (!trackEditor || disabled) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== 'z') return;
      event.preventDefault();
      if (event.shiftKey) redo();
      else undo();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [disabled, redo, trackEditor, undo]);

  const applyPreset = async (presetId: string) => {
    const seq = ++generateSeq.current;
    setSaveError('');
    try {
      const generated = await getDefaultAnimationSpec(identity, scope, presetId);
      if (seq !== generateSeq.current) return;
      // 保留当前并发版本号：默认规格是"未保存的草案"，它的 revision 取决于服务器上那份。
      // 再把入场时机搬过去：换风格只该换「怎么出现」，不该把复核过的绑定一起丢掉。
      const next = { ...carryBindings(draft, draftOf(generated)), revision: saved?.revision ?? 0 };
      setDraft(next);
      // 这一次生成没跑预填，旧的 prefill 说明（分档、条数）描述的是上一次生成，
      // 留着它会让面板拿着过期数字说"绑定 53 条"。分档是生成事件的事实，不是规格字段。
      setPrefillInfo(null);
      setPrefillTracks(null);
    } catch (err) {
      if (seq !== generateSeq.current) return;
      setSaveError(errorText(err, t));
    }
  };

  // runPrefill 按讲稿生成绑定（tasks 4.1 的入口）。
  //
  // 只在还没有已保存规格时可用：它产出的是「这一页的起点」，不是对现有绑定的合并。
  // 对已有规格跑一遍会整体替换人工确认过的绑定，而规格里的「人工权威」不允许。
  const runPrefill = async () => {
    const seq = ++generateSeq.current;
    setSaveError('');
    setPrefilling(true);
    try {
      const generated = await getDefaultAnimationSpec(identity, scope, draft?.presetId ?? '', true);
      if (seq !== generateSeq.current) return;
      const next = draftOf(generated);
      setDraft({ ...next, revision: saved?.revision ?? 0 });
      setPrefillInfo(generated.prefill ?? null);
      setPrefillTracks(prefillBindings(next).length);
      setPrefillFilter('');
    } catch (err) {
      if (seq !== generateSeq.current) return;
      setSaveError(errorText(err, t));
    } finally {
      if (seq === generateSeq.current) setPrefilling(false);
    }
  };

  const save = useCallback(async (): Promise<AnimationSpec | null> => {
    if (!draft) return null;
    setSaving(true);
    setSaveError('');
    try {
      const next = await saveAnimationSpec(identity, scope, savePayloadOf(draft));
      setSaved(next);
      setDraft(draftOf(next));
      setConflict(null);
      setSavedAt(Date.now());
      return next;
    } catch (err) {
      if (err instanceof AnimationConflictError) {
        // 冲突不是"保存失败"：服务器那份还活着，用户的选择决定谁留下。
        setConflict(err.latest);
      } else {
        setSaveError(errorText(err, t));
      }
      return null;
    } finally {
      setSaving(false);
    }
  }, [draft, identity, scope, t]);

  const takeLatest = () => {
    const latest = conflict;
    setConflict(null);
    setSaved(latest);
    setDraft(latest ? draftOf(latest) : null);
  };

  const overwrite = async () => {
    const latest = conflict;
    setConflict(null);
    if (!draft) return;
    // 覆盖 = 用服务器的最新版本号作为 expectedRevision，再提交我的内容。
    setSaving(true);
    setSaveError('');
    try {
      const next = await saveAnimationSpec(identity, scope, savePayloadOf({ ...draft, revision: latest?.revision ?? draft.revision }));
      setSaved(next);
      setDraft(draftOf(next));
      setSavedAt(Date.now());
    } catch (err) {
      setSaveError(errorText(err, t));
      setDraft(draft);
    } finally {
      setSaving(false);
    }
  };

  const runPreview = async () => {
    setPreviewError('');
    if (!timelineKey) {
      setPreviewError(t('editor.animationPreviewNeedsVoice'));
      return;
    }
    // 预览渲的是**已保存的规格**（服务端按规格身份取，不接受请求体里的草案）。
    // 有未保存改动就先存一次：否则用户看到的是上一版动效，会以为"改了没生效"。
    let current = saved;
    if (dirty) current = await save();
    if (!current) return;

    setPreviewing(true);
    try {
      const result = await previewAnimation(identity, scope, timelineKey);
      setPreviewURL((prev) => {
        if (prev) URL.revokeObjectURL(prev);
        return result.url;
      });
      setPreviewLayers(result.layers);
    } catch (err) {
      setPreviewError(previewErrorText(err, t));
    } finally {
      setPreviewing(false);
    }
  };

  const elementTracks = draft?.tracks.filter((track) => track.kind !== 'page') ?? [];
  const pageTracks = draft?.tracks.filter((track) => track.kind === 'page') ?? [];

  // 预填复核的来源判定与排序都在 animationPrefill（纯函数，node 用例钉住）。
  const bindings = prefillBindings(draft);
  const groups = sourceGroups(bindings);
  const rows = reviewOrder(filterBySource(bindings, prefillFilter));
  // 分档是**一次生成**的事实（band 不是规格字段），所以只认这次会话拿到的 DTO：
  // 重开面板时只剩来源与置信度，不能再声称"低置信"或"结构化"。
  const lowConfidence = prefillInfo?.band === 'low-confidence';
  const sourceLabel = (source: string): string => {
    const key = prefillSourceKey(source);
    // 未登记的来源显示原始 id：服务端加了新规则时，宁可让人看到 "layout" 这样的标识，
    // 也不要错配到另一条文案上（那会让人照着错的提示核对）。
    return key ? t(key) : source;
  };
  const segmentIndexOf = (segmentId: string): number | null => {
    const hit = anchors?.segments.find((item) => item.segmentId === segmentId);
    return hit ? hit.index : null;
  };
  // 撤销是一次编辑：先压栈再改草案。撤销本身必须可撤销，否则"点错了"就只剩重跑预填。
  const applyRevoked = (next: { spec: AnimationSpec; count: number }) => {
    if (next.count === 0) return;
    beginEdit();
    setDraft(next.spec);
  };
  const revokeWholeSource = () => {
    if (!draft) return;
    const next = revokeSource(draft, prefillFilter);
    if (next.count === 0) return;
    applyRevoked(next);
    // 这一组被撤空后筛选 chip 会消失；留在不存在的来源上会得到一片永远空白的列表。
    const group = groups.find((item) => item.source === prefillFilter);
    if (group && group.count === next.count) setPrefillFilter('');
  };

  return (
    <aside
      className="properties-drawer animation-drawer"
      role="dialog"
      aria-modal="true"
      aria-label={t('editor.animationTitle')}
      ref={dialogRef}
      onClick={(event) => event.stopPropagation()}
    >
      <header>
        <div>
          <span className="eyebrow">{t('editor.animationEyebrow')}</span>
          <h2>{t('editor.animationTitle')}</h2>
          {slideTitle && <p className="animation-slide">{slideTitle}</p>}
        </div>
        <button type="button" className="dialog-close-icon" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}>
          ×
        </button>
      </header>

      {loading && <p className="narration-note">{t('editor.animationLoading')}</p>}

      {!loading && loadError && (
        <div className="load-failure" role="alert">
          <p className="form-error">{loadError}</p>
          <button type="button" onClick={() => void load()}>{t('common.retry')}</button>
        </div>
      )}

      {!loading && !loadError && (
        <>
          {state.editDisabled && state.editDisabledReasonKey && (
            <p className="perm-hint">{t(state.editDisabledReasonKey)}</p>
          )}
          {/* 页时长为 0 = 还没有配音：既不能生成默认规格，也不能预览。
              在"还没选风格"时也要说，否则用户点第一个预设才撞上 422。 */}
          {(state.needsVoice || (anchors !== null && anchors.durationMs <= 0)) && (
            <p className="narration-note warn">{t('editor.animationNeedsVoice')}</p>
          )}

          {/* 第一级：风格。没有规格时这里就是"开始"入口。 */}
          <section className="animation-block">
            <span className="eyebrow">{t('editor.animationPreset')}</span>
            <div className="animation-presets" role="radiogroup" aria-label={t('editor.animationPreset')}>
              {(anchors?.presets ?? []).map((preset) => {
                const active = draft?.presetId === preset.id;
                return (
                  <button
                    key={preset.id}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    className={`animation-preset${active ? ' active' : ''}`}
                    disabled={disabled}
                    title={preset.description}
                    onClick={() => void applyPreset(preset.id)}
                  >
                    <strong>{preset.label}</strong>
                    <span>{preset.description}</span>
                  </button>
                );
              })}
            </div>
            {!draft && <p className="form-hint">{t('editor.animationNoSpec')}</p>}
          </section>

          {/* 讲稿绑定预填的入口（tasks 4.1）。单独一段而不是塞在风格里：
              它与"选风格"是两个决定（怎么出现 / 什么时候出现），混在一起会让人以为
              选预设就等于按讲稿绑定。
              只在还没有已保存规格时可用——它产出的是「这一页的起点」，不是合并。 */}
          <section className="animation-block">
            <span className="eyebrow">{t('editor.animationPrefillTitle')}</span>
            <div className="animation-prefill-action">
              <button
                type="button"
                className="button-ghost"
                disabled={disabled || prefilling || saved !== null || (anchors !== null && anchors.durationMs <= 0)}
                onClick={() => void runPrefill()}
              >
                {prefilling
                  ? t('editor.animationPrefillRunning')
                  : prefillInfo
                    ? t('editor.animationPrefillRegenerate')
                    : t('editor.animationPrefillAction')}
              </button>
            </div>
            {saved !== null && <p className="form-hint">{t('editor.animationPrefillNeedsFresh')}</p>}
          </section>

          {draft && (
            <>
              {/* 强度：只放大位移/缩放幅度，不动时长（时长归规格，见设计 ADR）。 */}
              <section className="animation-block">
                <label className="animation-intensity">
                  <span>{t('editor.animationIntensity')}</span>
                  <strong>{draft.intensity.toFixed(2)}×</strong>
                  <input
                    type="range"
                    min={0.5}
                    max={1.5}
                    step={0.05}
                    value={draft.intensity}
                    disabled={disabled}
                    onChange={(event) => setDraft(withIntensity(draft, Number(event.currentTarget.value)))}
                  />
                </label>
                <p className="form-hint">{t('editor.animationIntensityHint')}</p>
              </section>

              {/* 第二级：逐条要点的出现时机。 */}
              <section className="animation-block">
                <div className="animation-block-head">
                  <span className="eyebrow">{t('editor.animationTracks')}</span>
                  <span className="animation-summary">
                    {t('editor.animationSummary', { animated: summary.animatedBullets, total: summary.bullets })}
                  </span>
                </div>
                {elementTracks.length === 0 && <p className="form-hint">{t('editor.animationNoElements')}</p>}
                <ul className="animation-tracks">
                  {elementTracks.map((track) => {
                    const entrance = track.clips.find((clip) => clip.trigger === TRIGGER_ENTER || segmentOfTrigger(clip.trigger) !== '');
                    const trigger = entrance?.trigger ?? TRIGGER_ENTER;
                    const segment = segmentOfTrigger(trigger);
                    return (
                      <li key={track.elementId} className="animation-track">
                        <span className="animation-track-label" title={anchorLabel(anchors, track.elementId, track.elementId)}>
                          {anchorLabel(anchors, track.elementId, track.elementId)}
                        </span>
                        <select
                          value={segment ? `seg:${segment}` : TRIGGER_ENTER}
                          disabled={disabled}
                          aria-label={t('editor.animationTrigger')}
                          onChange={(event) => {
                            const value = event.currentTarget.value;
                            const nextTrigger = value === TRIGGER_ENTER ? TRIGGER_ENTER : segmentTrigger(value.slice('seg:'.length));
                            setDraft(withTrackTrigger(draft, track.elementId, nextTrigger));
                          }}
                        >
                          <option value={TRIGGER_ENTER}>{t('editor.animationTriggerEnter')}</option>
                          {(anchors?.segments ?? []).map((item) => (
                            <option key={item.segmentId} value={`seg:${item.segmentId}`}>
                              {t('editor.animationTriggerSegment', { index: item.index })}
                            </option>
                          ))}
                        </select>
                      </li>
                    );
                  })}
                </ul>
                {pageTracks.length > 0 && (
                  <p className="form-hint">
                    {t('editor.animationPageTrack', { count: pageTracks.reduce((n, track) => n + track.clips.length, 0) })}
                  </p>
                )}
              </section>

              {/* 预填复核（tasks 4.1–4.3）：机器猜的绑定按来源摊开，撤销以"组"为单位。
                  没有来源标注的页面不该出现这一块——空表只会让人以为功能坏了。 */}
              {(bindings.length > 0 || prefillInfo !== null) && (
                <section className={`animation-block animation-prefill${lowConfidence ? ' low-confidence' : ''}`}>
                  <div className="animation-block-head">
                    <span className="eyebrow">{t('editor.animationPrefillReview')}</span>
                    {bindings.length > 0 && (
                      <span className="animation-summary">{t('editor.animationPrefillCount', { count: bindings.length })}</span>
                    )}
                  </div>

                  {/* 预填被跳过的原因必须说出来：最坏的结果不是绑错几条（那看得见），
                      而是点了按钮之后什么都没变，用户以为功能没做。 */}
                  {prefillInfo !== null && !prefillInfo.applied && (
                    <p className="prefill-warn">{t('editor.animationPrefillSkipped')}</p>
                  )}

                  {/* 低置信的原因必须分开说：script 档是讲稿没把结构讲出来（版面没问题），
                      沿用「没有检测到结构化版面信号」等于对用户说假话，还会把他引去重排 PPT。
                      no-script 档不在这里说：上面那条「预填已跳过」已经讲了同一件事
                      （这一页没有讲稿分段），再叠一句只是把同一句话说两遍。 */}
                  {lowConfidence && prefillInfo?.bandReason !== 'no-script' && (
                    <p className="prefill-warn">{t(prefillLowConfidenceKey(prefillInfo?.bandReason))}</p>
                  )}

                  {prefillInfo?.band === 'structured' && (prefillInfo.signals?.length ?? 0) > 0 && (
                    <p className="form-hint">
                      {t('editor.animationPrefillSignals', {
                        signals: (prefillInfo.signals ?? [])
                          .map((signal) => {
                            const key = prefillSignalKey(signal);
                            return key ? t(key) : signal;
                          })
                          .join(' / ')
                      })}
                    </p>
                  )}

                  {/* 绑定数与轨道数的差额要说清楚：无文字容器（卡片底、分隔线）绑定成立，
                      但预设没给这类元素挂动效——不然"绑定 53 条"会被当成"产物 53 条"。 */}
                  {prefillInfo !== null && prefillInfo.applied && prefillTracks !== null && prefillInfo.bound !== prefillTracks && (
                    <p className="form-hint">
                      {t('editor.animationPrefillContainerGap', {
                        bound: prefillInfo.bound,
                        tracks: prefillTracks,
                        gap: Math.abs(prefillInfo.bound - prefillTracks)
                      })}
                    </p>
                  )}

                  {bindings.length > 0 && (
                    <>
                      <div className="prefill-sources" role="group" aria-label={t('editor.animationPrefillFilter')}>
                        <button
                          type="button"
                          className={`prefill-source${prefillFilter === '' ? ' active' : ''}`}
                          aria-pressed={prefillFilter === ''}
                          onClick={() => setPrefillFilter('')}
                        >
                          {t('editor.animationPrefillAll', { count: bindings.length })}
                        </button>
                        {groups.map((group) => (
                          <button
                            key={group.source}
                            type="button"
                            className={`prefill-source${prefillFilter === group.source ? ' active' : ''}`}
                            aria-pressed={prefillFilter === group.source}
                            onClick={() => setPrefillFilter(group.source)}
                          >
                            {sourceLabel(group.source)}
                            <span className="prefill-source-count">{group.count}</span>
                          </button>
                        ))}
                      </div>

                      <ul className="prefill-list">
                        {rows.map((row) => {
                          const index = segmentIndexOf(row.segmentId);
                          const where = row.segmentId
                            ? index === null
                              ? row.segmentId
                              : t('editor.animationTriggerSegment', { index })
                            : t('editor.animationTriggerEnter');
                          return (
                            <li key={row.elementId} className="prefill-row">
                              <span
                                className={`prefill-score${row.hasConfidence ? '' : ' none'}`}
                                title={row.hasConfidence ? t('editor.animationPrefillScoreTitle') : t('editor.animationPrefillNoScoreTitle')}
                              >
                                {row.hasConfidence ? row.confidence.toFixed(2) : '—'}
                              </span>
                              <span className="prefill-main">
                                <span className="prefill-label" title={anchorLabel(anchors, row.elementId, row.elementId)}>
                                  {anchorLabel(anchors, row.elementId, row.elementId)}
                                </span>
                                <span className="prefill-meta">
                                  {sourceLabel(row.source)} · {where}
                                </span>
                              </span>
                              <button
                                type="button"
                                className="button-ghost prefill-revoke"
                                disabled={disabled}
                                title={t('editor.animationPrefillRevokeOneHint')}
                                onClick={() => applyRevoked(revokeBinding(draft, row.elementId))}
                              >
                                {t('editor.animationPrefillRevokeOne')}
                              </button>
                            </li>
                          );
                        })}
                      </ul>

                      <div className="prefill-actions">
                        {prefillFilter !== '' && (
                          <button type="button" className="button-ghost" disabled={disabled} onClick={revokeWholeSource}>
                            {t('editor.animationPrefillRevokeSource', {
                              source: sourceLabel(prefillFilter),
                              count: rows.length
                            })}
                          </button>
                        )}
                        <p className="form-hint">{t('editor.animationPrefillOrderHint')}</p>
                      </div>
                    </>
                  )}
                </section>
              )}

              {/* 第三级：元素级轨道时间轴（受 capabilities.trackEditor 控制）。
                  未开通时只说一句实话，不渲染一个点了没反应的占位控件。 */}
              <section className="animation-block">
                <div className="animation-block-head">
                  <span className="eyebrow">{t('editor.animationTrackTimeline')}</span>
                  {trackEditor && (
                    <div className="track-history-actions">
                      <button type="button" className="button-ghost" disabled={disabled || history.past.length === 0} onClick={undo}>
                        {t('editor.animationUndo')}
                      </button>
                      <button type="button" className="button-ghost" disabled={disabled || history.future.length === 0} onClick={redo}>
                        {t('editor.animationRedo')}
                      </button>
                    </div>
                  )}
                </div>
                {!trackEditor ? (
                  <p className="form-hint">{t('editor.animationTrackLocked')}</p>
                ) : anchors !== null && anchors.durationMs <= 0 ? (
                  // 页时长未知时拖动没有参照系：允许拖就是一个必然被服务端拒绝的输入。
                  <p className="form-hint">{t('editor.animationTrackNeedsVoice')}</p>
                ) : (
                  <AnimationTrackTimeline
                    spec={draft}
                    anchors={anchors}
                    durationMs={anchors?.durationMs ?? 0}
                    disabled={disabled}
                    onBeforeEdit={beginEdit}
                    onChange={setDraft}
                    t={t}
                  />
                )}
              </section>

              {conflict && (
                <section className="animation-conflict" role="alert">
                  <strong>{t('editor.animationConflictTitle')}</strong>
                  <p>
                    {t('editor.animationConflictBody', {
                      local: draft.revision,
                      latest: conflict.revision
                    })}
                  </p>
                  <div className="draft-actions">
                    <button type="button" className="primary" onClick={() => void overwrite()} disabled={saving}>
                      {t('editor.animationConflictOverwrite')}
                    </button>
                    <button type="button" className="button-ghost" onClick={takeLatest} disabled={saving}>
                      {t('editor.animationConflictTakeLatest')}
                    </button>
                  </div>
                </section>
              )}

              {saveError && <p className="form-error" role="alert">{saveError}</p>}

              <div className="draft-actions animation-actions">
                <button type="button" className="primary" disabled={disabled || !dirty} onClick={() => void save()}>
                  {saving ? t('editor.animationSaving') : t('editor.animationSave')}
                </button>
                <button type="button" className="button-ghost" disabled={disabled || previewing || !saved} onClick={() => void runPreview()}>
                  {previewing ? t('editor.animationPreviewing') : t('editor.animationPreview')}
                </button>
                <span className={`animation-status${dirty ? ' dirty' : ''}`} role="status">
                  {dirty ? t('editor.animationDirty') : savedAt > 0 ? t('editor.animationSaved') : ''}
                </span>
              </div>

              {!timelineKey && <p className="form-hint">{t('editor.animationPreviewNeedsVoice')}</p>}
              {previewError && <p className="form-error" role="alert">{previewError}</p>}
              {previewURL && (
                <section className="animation-preview">
                  <video src={previewURL} controls autoPlay muted loop playsInline />
                  <p className="form-hint">
                    {previewLayers > 0
                      ? t('editor.animationPreviewLayers', { count: previewLayers })
                      : t('editor.animationPreviewStatic')}
                  </p>
                </section>
              )}
            </>
          )}
        </>
      )}
    </aside>
  );
}

// errorText 把后端信封翻成一句用户能读懂的话。
//
// 规则编号（V0–V12）优先映射成本地文案，未登记的编号回落到服务端 message：
// 显示一句英文总比"错配到另一条规则"好——后者会让用户照着错的提示改数据。
function errorText(err: unknown, t: (key: string) => string): string {
  if (err instanceof ConnectError) {
    // 规则编号优先：服务端那句话可能是英文实现细节，本地文案才是给用户看的。
    const ruleKey = animationRuleKey(err.rule);
    if (ruleKey) return t(ruleKey);
    if (err.code === 'duration_unknown') return t('editor.animationNeedsVoice');
    if (err.code === 'feature_unavailable') return t('editor.animationUnavailable');
    if (err.code === 'page_not_found') return t('editor.animationPageMissing');
    return err.message || t('editor.animationSaveFailed');
  }
  return err instanceof Error && err.message ? err.message : t('editor.animationSaveFailed');
}

// previewErrorText 单独一份映射：预览的失败原因与保存完全不同，
// 混用会让"超时"显示成"保存失败"，用户以为是数据问题。
function previewErrorText(err: unknown, t: (key: string) => string): string {
  if (err instanceof ConnectError) {
    switch (err.code) {
      case 'preview_timeout':
      case 'http_504':
        return t('editor.animationPreviewTimeout');
      case 'no_animation':
        return t('editor.animationPreviewNoAnimation');
      case 'duration_unknown':
        return t('editor.animationPreviewNeedsVoice');
      case 'page_image_missing':
        return t('editor.animationPreviewNoImage');
      case 'renderer_unavailable':
      case 'http_502':
        return t('editor.animationPreviewServiceDown');
      case 'compile_failed':
        return `${t('editor.animationPreviewFailed')}: ${err.message}`;
      default:
        return t('editor.animationPreviewFailed');
    }
  }
  return t('editor.animationPreviewFailed');
}
