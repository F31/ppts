#!/usr/bin/env node
/**
 * animation-check —— 逐页动画编辑的纯状态门禁（tasks 3.1）+ 导出降级文案（tasks 4.4/4.5）。
 *
 * 与 core-check/polling-check 同一风格：node --experimental-strip-types 直接加载
 * src/animationState.ts，测的就是组件真正调用的那份代码，不存在"测试抄一份逻辑"。
 *
 * 用法：node --experimental-strip-types animation-check.mjs
 */
import test from 'node:test';
import assert from 'node:assert/strict';

const m = await import('./src/animationState.ts');
const track = await import('./src/animationTrack.ts');
const {
  clampIntensity,
  editorStateOf,
  draftOf,
  withIntensity,
  withTrackTrigger,
  segmentOfTrigger,
  segmentTrigger,
  isDirty,
  savePayloadOf,
  animationRuleKey,
  summarize,
  anchorLabel
} = m;

const baseSpec = () => ({
  slideId: 'slide-1',
  language: 'zh-CN',
  sourceRevisionNo: 0,
  revision: 3,
  status: 'draft',
  presetId: 'tech-dynamic',
  intensity: 1.15,
  tracks: [
    {
      elementId: 'title-1',
      kind: 'title',
      clips: [{ id: 'c1', type: 'fadeIn', startMs: 0, durationMs: 400, easing: 'easeOut', trigger: 'onEnter' }]
    },
    {
      elementId: 'bullet-1',
      kind: 'bullet',
      clips: [
        { id: 'c2', type: 'slideIn', startMs: 0, durationMs: 600, easing: 'easeOut', trigger: 'onEnter', params: { direction: 'up' } },
        { id: 'c3', type: 'highlight', startMs: 900, durationMs: 600, easing: 'linear', trigger: 'atTime:900' }
      ]
    }
  ]
});

const baseAnchors = () => ({
  slideId: 'slide-1',
  language: 'zh-CN',
  durationMs: 8000,
  elements: [
    { id: 'title-1', kind: 'title', text: '季度回顾', index: 1 },
    { id: 'bullet-1', kind: 'bullet', text: '收入增长 18%', index: 2, bulletIndex: 1 },
    { id: 'bullet-2', kind: 'bullet', index: 3, bulletIndex: 2 }
  ],
  segments: [
    { segmentId: 'seg-1', index: 1 },
    { segmentId: 'seg-2', index: 2 }
  ],
  presets: [
    { id: 'business-calm', label: '商务稳重', description: '', intensity: 1 },
    { id: 'tech-dynamic', label: '科技动感', description: '', intensity: 1.15 }
  ]
});

test('强度夹紧到 [0.5, 1.5] 并保留两位小数', () => {
  assert.equal(clampIntensity(0.1), 0.5);
  assert.equal(clampIntensity(2), 1.5);
  assert.equal(clampIntensity(Number.NaN), 1);
  // 滑块 step=0.05 的浮点误差不能进规格：同一档强度出现两种写法会算出两个产物键。
  assert.equal(clampIntensity(1.1500000000000001), 1.15);
  assert.equal(clampIntensity(1.234), 1.23);
});

test('锁定态只读且不能预览之外的写入', () => {
  const locked = { ...baseSpec(), status: 'locked' };
  const s = editorStateOf(locked, true);
  assert.equal(s.readOnly, true);
  assert.equal(s.editDisabled, true);
  assert.equal(s.editDisabledReasonKey, 'editor.animationLocked');

  const none = editorStateOf(null, true);
  assert.equal(none.state, 'none');
  assert.equal(none.canPreview, false, '没有规格时无从预览');
  assert.equal(none.editDisabled, false);

  const noPerm = editorStateOf(baseSpec(), false);
  assert.equal(noPerm.editDisabled, true);
  assert.equal(noPerm.editDisabledReasonKey, 'perm.needEditorExport');

  // 时长未知（还没配音）不是"不能编辑"，只是要提示渲染前会重校验。
  const unknown = editorStateOf({ ...baseSpec(), needsDurationRecheck: true }, true);
  assert.equal(unknown.needsVoice, true);
  assert.equal(unknown.editDisabled, false);
});

test('改锚点只动入场片段，不动 atTime 的强调', () => {
  const draft = withTrackTrigger(baseSpec(), 'bullet-1', segmentTrigger('seg-2'));
  const track = draft.tracks.find((t) => t.elementId === 'bullet-1');
  assert.equal(track.clips[0].trigger, 'withSegment:seg-2');
  // 强调片段挂的是显式时刻：把它也重锚等于顺手改写了整条时间轴。
  assert.equal(track.clips[1].trigger, 'atTime:900');
  const other = draft.tracks.find((t) => t.elementId === 'title-1');
  assert.equal(other.clips[0].trigger, 'onEnter', '别的轨道不应被顺手改掉');
  assert.equal(segmentOfTrigger(track.clips[0].trigger), 'seg-2');
  assert.equal(segmentOfTrigger('onEnter'), '');
});

test('withTrackTrigger 不改原对象（取消编辑要能真的还原）', () => {
  const spec = baseSpec();
  const draft = draftOf(spec);
  withTrackTrigger(draft, 'bullet-1', segmentTrigger('seg-1'));
  assert.equal(spec.tracks[1].clips[0].trigger, 'onEnter', '原规格被改写了');
  const intensity = withIntensity(draft, 0.8);
  assert.equal(intensity.intensity, 0.8);
  assert.equal(draft.intensity, 1.15);
});

test('脏判据只看产物相关字段', () => {
  const spec = baseSpec();
  assert.equal(isDirty(draftOf(spec), spec), false);
  // needsDurationRecheck 是服务端提示：它一变就显示"未保存"，用户会以为自己的编辑丢了。
  assert.equal(isDirty({ ...draftOf(spec), needsDurationRecheck: true }, spec), false);
  assert.equal(isDirty({ ...draftOf(spec), intensity: 0.9 }, spec), true);
  const moved = withTrackTrigger(draftOf(spec), 'bullet-1', segmentTrigger('seg-2'));
  assert.equal(isDirty(moved, spec), true);
  // 参数改了也要算脏：params 参与编译（方向、位移），漏掉它就是"改了没保存"。
  const tweaked = draftOf(spec);
  tweaked.tracks[1].clips[0].params = { direction: 'left' };
  assert.equal(isDirty(tweaked, spec), true);
});

test('保存载荷带 expectedRevision（乐观并发）', () => {
  const payload = savePayloadOf({ ...baseSpec(), intensity: 1.3000000000000003 });
  assert.equal(payload.expectedRevision, 3);
  assert.equal(payload.intensity, 1.3);
  assert.equal(payload.tracks.length, 2);
  assert.equal('slideId' in payload, false, '页身份走 URL，不该重复出现在载荷里');
  assert.equal('revision' in payload, false);
});

test('规则编号映射，未登记编号返回空串', () => {
  assert.equal(animationRuleKey('v3'), 'editor.animationRuleV3');
  assert.equal(animationRuleKey('V7'), 'editor.animationRuleV7');
  assert.equal(animationRuleKey('v11'), 'editor.animationRuleV11');
  // 返回空串让调用方回落服务端 message：错配到另一条文案比显示英文更糟。
  assert.equal(animationRuleKey('V99'), '');
  assert.equal(animationRuleKey(undefined), '');
});

test('摘要只数元素级轨道，要点分母来自锚点', () => {
  const summary = summarize(baseSpec(), baseAnchors());
  assert.equal(summary.tracks, 2);
  assert.equal(summary.bullets, 2);
  assert.equal(summary.animatedBullets, 1);
  assert.equal(summary.segmentAligned, false);

  const aligned = summarize(withTrackTrigger(baseSpec(), 'bullet-1', segmentTrigger('seg-1')), baseAnchors());
  assert.equal(aligned.segmentAligned, true);

  const pageOnly = summarize({ ...baseSpec(), tracks: [{ elementId: '', kind: 'page', clips: [{ id: 'p', type: 'fadeIn', startMs: 0, durationMs: 300, easing: 'linear', trigger: 'onEnter' }] }] }, baseAnchors());
  assert.equal(pageOnly.tracks, 0, '整页轨道没有对应锚点，算进去会让"3/5 条要点"数不出来');
});

test('锚点标签优先文字，其次要点序号', () => {
  const anchors = baseAnchors();
  assert.equal(anchorLabel(anchors, 'bullet-1', 'element'), '收入增长 18%');
  assert.equal(anchorLabel(anchors, 'bullet-2', 'element'), '#2');
  assert.equal(anchorLabel(anchors, 'ghost', '#unknown'), '#unknown');
  assert.equal(anchorLabel(null, 'ghost', 'x'), 'x');
});

// ---- 导出降级文案（tasks 4.4/4.5 的「提示明确」） ----
//
// 后端把「动画被摘掉」的原因码放在 animationDropped 里，前端负责翻成人话。
// 这段映射只允许有一份（src/animationText.ts）：对话框置灰的原因与导出后降级的
// 原因如果各写一遍，迟早出现「对话框说服务不可用、提示里说未开启」。
const text = await import('./src/animationText.ts');
// t 的替身：把 vars 原样拼回 key，这样断言能看出「原因有没有传进去」「jobId 有没有丢」。
const fakeT = (key, vars) => (vars ? `${key}(${Object.entries(vars).map(([k, v]) => `${k}=${v}`).join(',')})` : key);

test('原因码按前缀归一，详情串不进 UI', () => {
  const { animationReasonText } = text;
  assert.equal(animationReasonText('animation_disabled_by_deployment', fakeT), 'editor.exportAnimationNotEnabled');
  assert.equal(animationReasonText('animation_not_available', fakeT), 'editor.exportAnimationNotEnabled');
  assert.equal(animationReasonText('renderer_not_configured', fakeT), 'editor.exportAnimationNotConfigured');
  // renderer_unavailable 的详情里有内网地址与 errno，整段丢弃
  assert.equal(
    animationReasonText('renderer_unavailable: Get "http://10.0.0.5:8090/healthz": connection refused', fakeT),
    'editor.exportAnimationServiceDown'
  );
  assert.equal(animationReasonText('something_new', fakeT), 'editor.exportAnimationUnavailable');
  assert.equal(animationReasonText(undefined, fakeT), 'editor.exportAnimationUnavailable');
});

test('降级提示必须同时带上原因与任务号', () => {
  const { animationDroppedText } = text;
  const msg = animationDroppedText('renderer_unavailable: connection refused', 'job-42', fakeT);
  assert.ok(msg.startsWith('editor.exportAnimationDropped('), msg);
  assert.match(msg, /reason=editor\.exportAnimationServiceDown/, '原因必须翻成用户语言，而不是原样贴错误串');
  assert.match(msg, /jobId=job-42/, '不带任务号，用户要在任务中心猜是哪一条');
  assert.doesNotMatch(msg, /connection refused|10\.0\.0\.5/, '内网地址与 errno 不该出现在用户界面');
});

// ---- 成品包动画层：关键帧编译（M6-2）----
//
// 这里只测纯函数部分（compileKeyframes）。buildLayerNodes 需要 DOM 与 WAAPI，
// 那部分靠浏览器验收，不在这里假装能测。
const { compileKeyframes } = await import('./src/sharePlayer/animation.ts');

const slideInClip = (startMs, durationMs) => ({
  clipId: 'c1',
  effect: 'slideIn',
  startMs,
  durationMs,
  easing: { x1: 0.22, y1: 1, x2: 0.36, y2: 1 },
  props: [
    { name: 'opacity', from: 0, to: 1 },
    { name: 'translateY', from: 48, to: 0 }
  ]
});

test('关键帧覆盖整页：首尾 offset 必须是 0 与 1', () => {
  const frames = compileKeyframes([slideInClip(100, 700)], {
    fps: 30,
    pageDurationMs: 6000,
    reduceMotion: false
  });
  assert.ok(frames, '700ms 的入场应该能编译出关键帧');
  assert.equal(frames[0].offset, 0);
  assert.equal(frames[frames.length - 1].offset, 1);
  // 首帧是动画开始前的状态：不可见且偏移 48px（画布像素）。
  assert.equal(frames[0].opacity, '0');
  assert.match(frames[0].transform, /48/);
  // 末帧是落位后：完全可见、无位移。
  assert.equal(frames[frames.length - 1].opacity, '1');
});

test('offset 严格递增：重复 offset 会让画面跳一下', () => {
  const frames = compileKeyframes([slideInClip(0, 900)], {
    fps: 30,
    pageDurationMs: 900,
    reduceMotion: false
  });
  assert.ok(frames.length > 2);
  for (let i = 1; i < frames.length; i++) {
    assert.ok(
      frames[i].offset > frames[i - 1].offset,
      `offset 必须递增：${frames[i - 1].offset} -> ${frames[i].offset}`
    );
  }
});

test('采样步长等于成片帧率：帧时刻上的值与 MP4 同帧对齐', () => {
  // 30fps → 33.33ms 一个采样点。700ms 的 clip 应产生约 21 个区间内采样点。
  const frames = compileKeyframes([slideInClip(0, 700)], {
    fps: 30,
    pageDurationMs: 700,
    reduceMotion: false
  });
  assert.ok(frames.length >= 20 && frames.length <= 25, `采样点数量异常: ${frames.length}`);
});

test('reduced-motion 只去掉位移与缩放，opacity 与时长不变', () => {
  const normal = compileKeyframes([slideInClip(0, 700)], {
    fps: 30,
    pageDurationMs: 6000,
    reduceMotion: false
  });
  const reduced = compileKeyframes([slideInClip(0, 700)], {
    fps: 30,
    pageDurationMs: 6000,
    reduceMotion: true
  });
  // 时长（由 offset 与 pageDurationMs 决定）必须一致，否则音画错位。
  assert.equal(reduced.length, normal.length);
  assert.equal(reduced[0].offset, normal[0].offset);
  // 位移被去掉，但淡入保留。
  assert.equal(reduced[0].transform, 'translate(0, 0) scale(1, 1)');
  assert.equal(reduced[0].opacity, normal[0].opacity);
});

test('没有 clip 的层不编译关键帧：静态元素不该占一条 animation', () => {
  assert.equal(compileKeyframes([], { fps: 30, pageDurationMs: 6000, reduceMotion: false }), null);
  // 零长 clip 也不该产出：它没有可播的时间区间（终态由元素初始样式负责）。
  assert.equal(
    compileKeyframes([{ ...slideInClip(0, 0), props: [] }], {
      fps: 30,
      pageDurationMs: 6000,
      reduceMotion: false
    }),
    null
  );
});

// ─── 轨道编辑（M4 第 3 段）────────────────────────────────────────────────

test('拖动入场片段会改成 atTime，否则渲染器仍按 trigger 算起点（界面上动了、成片里没动）', () => {
  const spec = baseSpec();
  const moved = track.moveClipTo(spec, 'title-1', 0, 1200, 8000);
  const clip = moved.tracks[0].clips[0];
  assert.equal(clip.startMs, 1200);
  assert.equal(clip.trigger, 'atTime:1200', '派生起点必须显式化，否则拖动是假功能');
  // 原对象不变：撤销依赖"每次编辑产生新快照"。
  assert.equal(spec.tracks[0].clips[0].startMs, 0);
  assert.equal(spec.tracks[0].clips[0].trigger, 'onEnter');
});

test('拖动 withSegment 片段同样转 atTime（分段对齐也是派生起点）', () => {
  const spec = baseSpec();
  const aligned = track.moveClipTo(spec, 'bullet-1', 0, 2000, 8000);
  // bullet-1 的 c3 是 atTime:900，c2 是 onEnter；这里拖 c2。
  assert.equal(aligned.tracks[1].clips[0].trigger, 'atTime:2000');
  // 同一条轨道上的 atTime 片段不受影响。
  assert.equal(aligned.tracks[1].clips[1].startMs, 900);
  assert.equal(aligned.tracks[1].clips[1].trigger, 'atTime:900');
});

test('起点夹紧到 [0, 页时长-时长]，页时长未知时只夹下界', () => {
  const spec = baseSpec();
  const early = track.moveClipTo(spec, 'bullet-1', 0, -500, 8000);
  assert.equal(early.tracks[1].clips[0].startMs, 0);
  const late = track.moveClipTo(spec, 'bullet-1', 0, 99999, 8000);
  // 600ms 的 clip 在 8000ms 的页上，最晚从 7400 开始。
  assert.equal(late.tracks[1].clips[0].startMs, 7400);
  const unknownPage = track.moveClipTo(spec, 'bullet-1', 0, 99999, 0);
  assert.equal(unknownPage.tracks[1].clips[0].startMs, 99999, '没配音时不该假装知道页尾');
});

test('改时长夹在 [120, 3000]，且不把 trigger 转成 atTime', () => {
  const spec = baseSpec();
  const shorter = track.resizeClipTo(spec, 'title-1', 0, 10, 8000);
  assert.equal(shorter.tracks[0].clips[0].durationMs, 120);
  assert.equal(shorter.tracks[0].clips[0].trigger, 'onEnter', '只拉长/缩短不该脱离朗读对齐');
  const longer = track.resizeClipTo(spec, 'title-1', 0, 99999, 8000);
  assert.equal(longer.tracks[0].clips[0].durationMs, 3000);
  // 页面尾部放不下时**截断时长**，起点不动：用户拖的是右边缘，
  // 色块整体跳走会让人以为拖错了对象（第一版就是这么写的，这条用例守回来的路）。
  const tight = track.resizeClipTo(track.moveClipTo(spec, 'title-1', 0, 7500, 8000), 'title-1', 0, 3000, 8000);
  assert.equal(tight.tracks[0].clips[0].startMs, 7500, '改时长不该挪起点');
  assert.equal(tight.tracks[0].clips[0].durationMs, 500, '只剩 500ms 空间就截到 500');
  // 连最短时长都放不下时（只可能来自既有非法数据：时长 < 120 且贴着页尾）才回退到挪起点。
  // 正常拖动走不到这里——moveClipTo 的夹紧一定给当前时长留够空间。
  const invalid = baseSpec();
  invalid.tracks[0].clips[0] = { ...invalid.tracks[0].clips[0], startMs: 7960, durationMs: 40 };
  const cramped = track.resizeClipTo(invalid, 'title-1', 0, 3000, 8000);
  assert.equal(cramped.tracks[0].clips[0].durationMs, 120);
  assert.ok(cramped.tracks[0].clips[0].startMs + cramped.tracks[0].clips[0].durationMs <= 8000);
});

test('方向键 10ms / Shift 50ms，且两端都不越界', () => {
  const spec = baseSpec();
  assert.equal(track.nudgeClip(spec, 'bullet-1', 0, 1, false, 8000).tracks[1].clips[0].startMs, 10);
  assert.equal(track.nudgeClip(spec, 'bullet-1', 0, 1, true, 8000).tracks[1].clips[0].startMs, 50);
  assert.equal(track.nudgeClip(spec, 'bullet-1', 0, -1, false, 8000).tracks[1].clips[0].startMs, 0);
});

test('时间轴几何：位置与宽度都按页时长算，未知页时长给 0 宽度', () => {
  const clip = { type: 'fadeIn', startMs: 2000, durationMs: 1000, easing: 'linear', trigger: 'atTime:2000' };
  const geo = track.clipGeometry(clip, 8000);
  assert.equal(geo.leftPct, 25);
  assert.equal(geo.widthPct, 12.5);
  assert.deepEqual(track.clipGeometry(clip, 0), { leftPct: 0, widthPct: 0 });
  // 越界的 start 也不会画出超过 100% 的宽度（那会画到面板外面）。
  const overflow = track.clipGeometry({ ...clip, startMs: 7900, durationMs: 3000 }, 8000);
  assert.ok(overflow.leftPct + overflow.widthPct <= 100.0001);
});

test('撤销/重做：记录的是整份快照，栈深不超过 HISTORY_LIMIT', () => {
  let history = track.emptyHistory();
  const spec = baseSpec();
  // 连按 30 次（比栈深多），每一"步"记一次快照。
  for (let i = 0; i < 30; i++) history = track.recordHistory(history, { ...spec, revision: i });
  assert.equal(history.past.length, track.HISTORY_LIMIT);
  assert.ok(track.HISTORY_LIMIT >= 20, '设计要求撤销 ≥20 步');

  const first = track.undoHistory(history, spec);
  assert.ok(first);
  assert.equal(first.spec.revision, 29);
  assert.equal(first.history.future.length, 1);

  const back = track.redoHistory(first.history, first.spec);
  assert.ok(back);
  assert.equal(back.spec, spec);
});

test('撤销栈为空时不返回结果（调用方据此禁用按钮，而不是静默无效）', () => {
  assert.equal(track.undoHistory(track.emptyHistory(), baseSpec()), null);
  assert.equal(track.redoHistory(track.emptyHistory(), baseSpec()), null);
});

test('一次编辑后重做栈被清空：新分支不该还能撤回到旧的未来', () => {
  let history = track.recordHistory(track.emptyHistory(), baseSpec());
  const undone = track.undoHistory(history, baseSpec());
  assert.equal(undone.history.future.length, 1);
  history = track.recordHistory(undone.history, undone.spec);
  assert.equal(history.future.length, 0);
});

test('整页轨道不参与拖动（起点是页边界，没有可调空间）', () => {
  const spec = {
    ...baseSpec(),
    tracks: [...baseSpec().tracks, { elementId: 'page-1', kind: 'page', clips: [{ id: 'p1', type: 'pageFade', startMs: 0, durationMs: 400, easing: 'linear', trigger: 'onEnter' }] }]
  };
  assert.deepEqual(
    track.editableTracks(spec.tracks).map((item) => item.elementId),
    ['title-1', 'bullet-1']
  );
});

test('焦点切换点必须在关键帧里：否则网页从最后关键帧线性插到页末', async () => {
  // 页级 focus（IR 的 page.focus）在播放器里是「谁让位」，采样只覆盖 clip 活动区间
  // 时，切换点（1390/4500）会落在采样之外，压暗被拉长成整页。
  const { compileKeyframes } = await import('./src/sharePlayer/animation.ts');
  const ease = { x1: 0.2, y1: 0, x2: 0, y2: 1 };
  const clips = [
    { effect: 'fadeIn', startMs: 220, durationMs: 520, easing: ease, props: [{ name: 'opacity', from: 0, to: 1 }] }
  ];
  const focus = [
    { startMs: 1390, elementId: 'a' },
    { startMs: 4500, elementId: 'b' }
  ];
  const pageMs = 12005;
  const opts = { fps: 30, pageDurationMs: pageMs, reduceMotion: false, focus };
  const frames = compileKeyframes(clips, opts, 'a');
  assert.ok(frames && frames.length > 2);
  const at = (ms) => frames.some((f) => Math.abs(f.offset * pageMs - ms) <= 20);
  assert.ok(at(1390), '第 1 个切换点没有关键帧');
  assert.ok(at(4500), '第 2 个切换点没有关键帧');
  const last = frames[frames.length - 1];
  assert.ok(Number(last.opacity) < 0.6, `页末应已压暗，实际 ${last.opacity}`);
  assert.equal(Number(frames[0].opacity), 0, '页首还没开口，应当透明');
});
