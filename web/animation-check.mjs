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
import { readFile } from 'node:fs/promises';

const m = await import('./src/animationState.ts');
const track = await import('./src/animationTrack.ts');
const prefill = await import('./src/animationPrefill.ts');
const {
  prefillBindings,
  reviewOrder,
  sourceGroups,
  filterBySource,
  revokeSource,
  revokeBinding,
  carryBindings,
  defaultSpecQuery,
  prefillSourceKey,
  prefillSignalKey,
  prefillLowConfidenceKey,
  prefillModelFallbackKey,
  prefillWaitExpired,
  PREFILL_WAIT_BUDGET_MS
} = prefill;
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

test('没有渲染服务时，文案必须说明静态版没有转场（tasks 7.1）', async () => {
  // 转场与元素动画共用同一次合成（ADR-1），所以 backend=none 时转场一并失效。
  // 这件事只发生在用户看不见的后端，唯一能让用户不把它当 bug 的地方就是这句文案；
  // 而文案最容易在改措辞时被删掉。断言只要求「说了转场这回事」，不锁句子。
  const read = async (name) =>
    JSON.parse(await readFile(new URL(`./src/i18n/${name}`, import.meta.url), 'utf8'));
  const zh = await read('zh.json');
  const en = await read('en.json');
  assert.match(zh['editor.exportAnimationNotConfigured'], /转场/, '中文文案必须点明转场失效');
  assert.match(en['editor.exportAnimationNotConfigured'], /transition/i, 'English copy must name the transition loss');
});

test('降级提示必须同时带上原因与任务号', () => {
  const { animationDroppedText } = text;
  const msg = animationDroppedText('renderer_unavailable: connection refused', 'job-42', fakeT);
  assert.ok(msg.startsWith('editor.exportAnimationDropped('), msg);
  assert.match(msg, /reason=editor\.exportAnimationServiceDown/, '原因必须翻成用户语言，而不是原样贴错误串');
  assert.match(msg, /jobId=job-42/, '不带任务号，用户要在任务中心猜是哪一条');
  assert.doesNotMatch(msg, /connection refused|10\.0\.0\.5/, '内网地址与 errno 不该出现在用户界面');
});

// ---- 免费层时长超限（ADR-029 ②）----
//
// 后端返回 422 + {code, limitSeconds, actualSeconds}，message 是中文写死的。
// 直接把它显示出来，英文界面就会冒出中文——而这条错误恰恰带着用户唯一能据以行动的信息
// （差多少、改到多少能过），退回通用「导出失败」等于把它丢掉。
test('时长超限：数字取自错误信封，文案按语言本地化', () => {
  const { exportFailureText } = text;
  const err = {
    code: 'export_duration_exceeded',
    message: '成片时长 6 分 00 秒 超过当前套餐上限 3 分 00 秒；请精简讲稿，或升级套餐后再导出',
    details: { code: 'export_duration_exceeded', limitSeconds: 180, actualSeconds: 360 }
  };
  const zh = exportFailureText(err, fakeT, 'zh');
  assert.match(zh, /^editor\.exportDurationExceeded\(/, zh);
  assert.match(zh, /limit=3 分 00 秒/, '上限必须出现在文案里');
  assert.match(zh, /actual=6 分 00 秒/, '实际时长必须出现在文案里');

  const en = exportFailureText(err, fakeT, 'en');
  assert.match(en, /^editor\.exportDurationExceeded\(/, en);
  assert.match(en, /limit=3m 00s/, 'English copy must not borrow the Chinese duration format');
  assert.match(en, /actual=6m 00s/, 'English copy must not borrow the Chinese duration format');
  assert.doesNotMatch(en, /成片时长|套餐/, '英文界面不许出现服务端那句中文');
});

test('时长超限：不足一分钟不带「分」，四舍五入到整秒', () => {
  const { exportFailureText } = text;
  const err = (actual) => ({
    code: 'export_duration_exceeded',
    details: { limitSeconds: 60, actualSeconds: actual }
  });
  assert.match(exportFailureText(err(45), fakeT, 'zh'), /actual=45 秒/);
  assert.match(exportFailureText(err(45), fakeT, 'en'), /actual=45s/);
  // 后端给的是浮点（微秒换算），显示层四舍五入，不能出现 59.8 秒
  assert.match(exportFailureText(err(59.8), fakeT, 'zh'), /actual=1 分 00 秒/);
});

test('时长超限：数字缺失时仍走专用文案，不退回「导出失败」', () => {
  const { exportFailureText } = text;
  const bare = exportFailureText({ code: 'export_duration_exceeded' }, fakeT, 'en');
  assert.equal(bare, 'editor.exportDurationExceededBare',
    '退回通用失败会把「改小就能过」这条唯一有用的信息丢掉');
  assert.equal(exportFailureText({ code: 'export_duration_exceeded', details: { limitSeconds: 0, actualSeconds: 10 } }, fakeT, 'zh'),
    'editor.exportDurationExceededBare', '上限为 0 说明字段没填对，不能拼出「超过 0 秒」这种文案');
});

// ---- 成品包动画层：关键帧编译（M6-2）----
//
// 这里测纯函数部分（compileKeyframes）。buildLayerNodes 需要 DOM 与 WAAPI，
// 像素级验收靠浏览器与真片；但**编译结果**（节点定位、关键帧值）可以用最小 stub 测，
// 而且必须测：矩形焦点遮罩漏写 `position:absolute` 时，静态检查与类型检查全绿，
// 只有真浏览器里才会变成「整页均匀压暗、孔不存在」。这条用例让它在 CI 就红。
const { buildLayerNodes, compileKeyframes, needsLayerStage } = await import('./src/sharePlayer/animation.ts');

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

// ── G8 跨页延续（编辑层）──
test('continues 的 clip 不被夹到「页时长-时长」起点上界', () => {
  const spec = baseSpec();
  // bullet-1 第一条 c2 是 onEnter（600ms）；标记 continues 后拖到 7900 不应被拉回 7400。
  let s = track.setClipContinues(spec, 'bullet-1', 0, true);
  s = track.moveClipTo(s, 'bullet-1', 0, 7900, 8000);
  const clip = s.tracks[1].clips[0];
  assert.equal(clip.continues, true);
  assert.equal(clip.startMs, 7900, 'continues 起点可越过页尾，不被夹回');
  // 非 continues 仍被夹到 7400。
  const nonCont = track.moveClipTo(spec, 'bullet-1', 0, 7900, 8000);
  assert.equal(nonCont.tracks[1].clips[0].startMs, 7400);
});

test('continues 的 clip 拉长不被页尾空间截断', () => {
  const spec = baseSpec();
  const s = track.setClipContinues(spec, 'title-1', 0, true);
  // title-1 在 8000ms 页上，从 7500 开始拉长到 3000：非 continues 会截到 500；continues 应保持 3000。
  const moved = track.moveClipTo(s, 'title-1', 0, 7500, 8000);
  const grown = track.resizeClipTo(moved, 'title-1', 0, 3000, 8000);
  const clip = grown.tracks[0].clips[0];
  assert.equal(clip.durationMs, 3000, 'continues 时长不截断');
  assert.ok(clip.startMs + clip.durationMs > 8000, '延续部分越过页尾');
});

test('setClipContinues 只改布尔标记，不动其它字段', () => {
  const spec = baseSpec();
  const s = track.setClipContinues(spec, 'bullet-1', 1, true);
  const clip = s.tracks[1].clips[1];
  assert.equal(clip.continues, true);
  assert.equal(clip.trigger, 'atTime:900');
  assert.equal(spec.tracks[1].clips[1].continues, undefined, '原对象不变');
});

test('specFingerprint 与保存载荷把 continues 计入', () => {
  const spec = baseSpec();
  const on = track.setClipContinues(baseSpec(), 'bullet-1', 0, true);
  assert.equal(isDirty(on, spec), true, '切换 continues 应算脏');
  const payload = savePayloadOf(on);
  assert.equal(payload.tracks[1].clips[0].continues, true, '保存载荷含 continues');
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
  const { buildLayerNodes, compileKeyframes, needsLayerStage } = await import('./src/sharePlayer/animation.ts');
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

test('放手时刻也必须在关键帧里：否则「回到全亮」被插值抹成一整页渐变', async () => {
  // 分段之后，a 讲到 4402 就放手，b 到 5938 才开讲。若只采样切换点（5938），
  // 4402→5938 这段会被线性插值填成「从压暗慢慢变亮」——看起来像褪色。
  const { buildLayerNodes, compileKeyframes, needsLayerStage } = await import('./src/sharePlayer/animation.ts');
  const ease = { x1: 0.2, y1: 0, x2: 0, y2: 1 };
  const clips = [
    { effect: 'fadeIn', startMs: 220, durationMs: 520, easing: ease, props: [{ name: 'opacity', from: 0, to: 1 }] }
  ];
  const focus = [
    { startMs: 1772, elementId: 'a', endMs: 4402 },
    { startMs: 5938, elementId: 'b' }
  ];
  const pageMs = 9855;
  const opts = { fps: 30, pageDurationMs: pageMs, reduceMotion: false, focus };
  const frames = compileKeyframes(clips, opts, 'a');
  const at = (ms) => frames.some((f) => Math.abs(f.offset * pageMs - ms) <= 20);
  assert.ok(at(4402), '放手时刻没有关键帧');
  assert.ok(at(4402 + 240), '放手后的淡出终点没有关键帧');
  // 终点才是判据：放手那一刻仍然是压暗值（0.55），一个淡出之后才是 1。
  // 第一版把 dim 写成了 p，两个端点都成了 1——片子里的形状是「先更暗、然后突然全亮」
  // （0.95 → 0.63 → 0.56 → 1.00），眼睛看得出，但只看「最后是不是 1」是看不出的。
  // 取**离目标最近**的关键帧，不是第一个撞进 20ms 窗口的：过渡窗口现在按 8ms
  // 加密采样，窗口里会有好几个点，`find` 拿到的可能是淡出中的中间值。
  const nearest = (ms) =>
    frames.reduce((best, f) =>
      Math.abs(f.offset * pageMs - ms) < Math.abs(best.offset * pageMs - ms) ? f : best);
  const atEnd = nearest(4402);
  assert.ok(Math.abs(Number(atEnd.opacity) - 0.55) < 0.02,
    `放手那一刻应仍是压暗值 0.55，实际 ${atEnd.opacity}`);
  const relax = nearest(4402 + 240);
  assert.equal(Number(relax.opacity), 1, `放手后不透明度应为 1，实际 ${relax.opacity}`);
});

test('页内第一个焦点也要淡出：不能一帧就把整页压暗', async () => {
  // focusDim 的 i===0 分支原来直接写 1/0，于是 tMs 一过 startMs，整页从全亮
  // 跳到压暗只用一帧——50Hz 采样下也只有一个 20ms 窗口有变化（L 片页 2 的
  // 18.9s）。后面每次换人都有 240ms，唯独第一个没有。26 项判据都在问「此刻
  // 亮着几个」，没有一条问「怎么变过去的」，所以全绿也查不到。
  const { focusDim } = await import('../shared/motion.ts');
  const focus = [
    { startMs: 1000, elementId: 'bar0' },
    { startMs: 3000, elementId: 'bar1' }
  ];
  assert.deepEqual(focusDim(focus, 999), {}, '焦点开始前应当是整页全亮');
  const atStart = focusDim(focus, 1000);
  assert.equal(atStart.bar0, 0, '主角在开始那一刻就应当满墨');
  assert.equal(atStart.bar1, 0,
    `开始那一刻其余元素还应当是全亮（0），实际 ${atStart.bar1}——直接给 1 就是一帧压暗`);
  const mid = focusDim(focus, 1120);
  assert.ok(mid.bar1 > 0.6 && mid.bar1 < 0.98, `120ms 时应处于过渡中，实际 ${mid.bar1}`);
  assert.equal(focusDim(focus, 1240).bar1, 1, '240ms 后应当完全退后');
});

test('同一时刻只指一个：第 3 个焦点开始后，第 1 个元素必须已经退后', async () => {
  // 这条测的是 focusDim 的一个曾存在的 bug：`out[focus[0]] = 0` 少了 i===0 的条件，
  // 于是每帧都把第 1 个元素重新点成全亮，从第 3 个焦点起页面上有两根柱子同时满墨。
  // 肉眼看得出来（两根一样深），但没有任何判据问过「此刻亮着几个」。
  const { focusDim } = await import('../shared/motion.ts');
  const focus = [
    { startMs: 1772, elementId: 'bar0' },
    { startMs: 3572, elementId: 'bar1' },
    { startMs: 5938, elementId: 'bar2' },
    { startMs: 8208, elementId: 'bar3' }
  ];
  const dims = focusDim(focus, 6500);
  const bright = Object.entries(dims).filter(([, d]) => d <= 0.01).map(([k]) => k);
  assert.deepEqual(bright, ['bar2'], `6500ms 时应当只有 bar2 在亮，实际 ${JSON.stringify(bright)}`);
  assert.equal(dims.bar0, 1, `bar0 应当已退后，实际 ${dims.bar0}`);
  // 放手后的空档里一个都不指（全亮是缺省态，返回空表）。
  const gap = focusDim([{ startMs: 0, elementId: 'a', endMs: 400 }, { startMs: 900, elementId: 'b' }], 700);
  assert.deepEqual(gap, {}, '空档里不应有任何压暗');
});

// ── G8 跨页延续（播放器续播 / 单一求值器）──
// 延续段用「负起点平移求值」：次页段 StartMs = -offset，与原 clip 在「页缝 + localMs」处进度一致，
// cubic-bezier 缓动天然连续（见 design §2）。以下用例与 Go 侧 TestCompileContinuationSplitsAcrossPages
// 同语义，守住「页缝不跳变」。
const { evaluateLayer, cubicBezier } = await import('../shared/motion.ts');
const contEase = { x1: 0.22, y1: 1, x2: 0.36, y2: 1 };
const contProps = (from, to) => [
  { name: 'opacity', from, to },
  { name: 'translateY', from: 40, to: 0 }
];

test('跨页延续段用负起点时，compileKeyframes 不产出负 offset 关键帧', async () => {
  const clips = [
    { clipId: 'c1', effect: 'fadeIn', startMs: -3000, durationMs: 8000,
      easing: contEase, isContinuation: true, continuationOffsetMs: 3000,
      props: contProps(0, 1) }
  ];
  const frames = compileKeyframes(clips, { fps: 30, pageDurationMs: 10000, reduceMotion: false }, 'cont-0');
  assert.ok(frames && frames.length >= 2, '延续段应编译出关键帧');
  for (const f of frames) {
    assert.ok(f.offset >= 0 && f.offset <= 1, `offset 必须在 [0,1]，实得 ${f.offset}`);
  }
});

test('页缝处 continuation 段与原 clip 末态连续（evaluateLayer）', async () => {
  // 原 clip：页 i 从 7000ms 起、时长 8000ms（页长 10000ms → 溢出、offset=3000）。
  const source = { clips: [
    { clipId: 'c1', effect: 'fadeIn', startMs: 7000, durationMs: 8000, easing: contEase, props: contProps(0, 1) }
  ] };
  // 次页段：负起点 -3000，沿用完整 from→to。
  const cont = { clips: [
    { clipId: 'c1', effect: 'fadeIn', startMs: -3000, durationMs: 8000, easing: contEase,
      isContinuation: true, continuationOffsetMs: 3000, props: contProps(0, 1) }
  ] };

  // 页缝：页 i 末态（localMs=10000）→ 进度 (10000-7000)/8000 = 0.375；
  // 次页首态（localMs=0）→ 进度 (0+3000)/8000 = 0.375。必须相等。
  const seamN = evaluateLayer(source, 10000).opacity;
  const seamN1 = evaluateLayer(cont, 0).opacity;
  assert.ok(Math.abs(seamN - seamN1) < 1e-9, `页缝 opacity 不连续：${seamN} vs ${seamN1}`);

  // 缝后任意点也连续：次页 localMs=2000 对应原 clip 的 g=12000（页 i 末态 + 2000），
  // 即原 clip 在 (12000-7000)/8000 = 0.625 处；延续段 (2000+3000)/8000 = 0.625。
  const origAt = evaluateLayer(source, 12000).opacity;
  const contAt = evaluateLayer(cont, 2000).opacity;
  assert.ok(Math.abs(origAt - contAt) < 1e-9, `缝后 opacity 不连续：${origAt} vs ${contAt}`);

  // 与解析缓动一致：opacity = from + (to-from)*cubicBezier(ease, 0.625)。
  const want = 0 + (1 - 0) * cubicBezier(contEase, 0.625);
  assert.ok(Math.abs(contAt - want) < 1e-9, `opacity 与缓动不一致：${contAt} vs ${want}`);
});

test('旧播放器忽略 isContinuation 时次页段仍可见、不露白', async () => {
  // 旧播放器不识别 isContinuation：次页段退化普通层，但负起点求值仍给出可见终态，不崩不露白。
  const cont = { clips: [
    { clipId: 'c1', effect: 'fadeIn', startMs: -3000, durationMs: 8000, easing: contEase,
      isContinuation: true, continuationOffsetMs: 3000, props: contProps(0, 1) }
  ] };
  const atStart = evaluateLayer(cont, 0);
  assert.ok(
    Number.isFinite(atStart.opacity) && atStart.opacity > 0 && atStart.opacity <= 1,
    `次页首帧应可见，实得 opacity=${atStart.opacity}`
  );
  // 延续段结束后（localMs=5000）保持终态 opacity=1，不回跳为 0（不露白）。
  assert.equal(evaluateLayer(cont, 5000).opacity, 1, '延续段结束后应保持终态可见');
});

// ─── 讲稿绑定预填的复核层（tasks 4.1–4.3） ─────────────────────────────────
//
// 这一组测的是「复核 + 撤销」的行为，不是服务端规则。判据都落在"用户能不能看出来"上：
// 撤销了却仍然算脏、保存一次来源就丢、表里顺序每次都变——这些都是静默失败。

const prefillSpec = () => ({
  slideId: 'slide-1',
  language: 'zh-CN',
  sourceRevisionNo: 0,
  revision: 5,
  status: 'draft',
  presetId: 'business-calm',
  intensity: 1,
  tracks: [
    {
      elementId: 'bullet-1',
      kind: 'bullet',
      clips: [{ id: 'c1', type: 'fadeIn', startMs: 0, durationMs: 400, easing: 'easeOut', trigger: 'withSegment:seg-2', triggerSource: 'text', confidence: 0.42 }]
    },
    {
      elementId: 'card-1',
      kind: 'shape',
      clips: [{ id: 'c2', type: 'slideIn', startMs: 280, durationMs: 500, easing: 'easeOut', trigger: 'withSegment:seg-2', triggerSource: 'sibling' }]
    },
    {
      elementId: 'title-1',
      kind: 'title',
      clips: [{ id: 'c3', type: 'fadeIn', startMs: 0, durationMs: 400, easing: 'easeOut', trigger: 'withSegment:seg-1', triggerSource: 'prior' }]
    },
    {
      elementId: 'bullet-2',
      kind: 'bullet',
      clips: [
        { id: 'c4', type: 'fadeIn', startMs: 0, durationMs: 400, easing: 'easeOut', trigger: 'withSegment:seg-3', triggerSource: 'text', confidence: 0.31 },
        { id: 'c5', type: 'highlight', startMs: 900, durationMs: 600, easing: 'linear', trigger: 'atTime:900' }
      ]
    },
    { elementId: '', kind: 'page', clips: [{ id: 'c6', type: 'fadeIn', startMs: 0, durationMs: 300, easing: 'linear', trigger: 'onEnter' }] }
  ]
});

test('预填表只列带来源的入场 clip：整页轨道与人工强调都不算', () => {
  const items = prefillBindings(prefillSpec());
  assert.deepEqual(
    items.map((item) => item.elementId),
    ['bullet-1', 'card-1', 'title-1', 'bullet-2'],
    '整页轨道不该出现（它没有可复核的绑定），强调 clip 也不该重复列同一元素'
  );
  const card = items.find((item) => item.elementId === 'card-1');
  assert.equal(card.hasConfidence, false, 'sibling 没有字面依据：置信度必须是「不适用」，不是 0 分');
  assert.equal(card.confidence, 0);
  assert.equal(items.find((item) => item.elementId === 'bullet-1').segmentId, 'seg-2');
});

test('来源分组顺序固定：同块带出在标题先验与字面之前', () => {
  const groups = sourceGroups(prefillBindings(prefillSpec()));
  assert.deepEqual(groups, [
    { source: 'sibling', count: 1 },
    { source: 'prior', count: 1 },
    { source: 'text', count: 2 }
  ]);
});

test('复核排序：无字面依据的规则在前，字面规则按相似度升序', () => {
  const order = reviewOrder(prefillBindings(prefillSpec())).map((item) => `${item.source}:${item.elementId}`);
  assert.deepEqual(order, ['sibling:card-1', 'prior:title-1', 'text:bullet-2', 'text:bullet-1']);
  // 确定性：同一份规格连排两次结果逐条相等（表跳动会让人点错行）。
  const again = reviewOrder(prefillBindings(prefillSpec())).map((item) => item.elementId);
  assert.deepEqual(again, reviewOrder(prefillBindings(prefillSpec())).map((item) => item.elementId));
});

test('撤销一个来源：改回页首、清掉标注，其它来源不动', () => {
  const { spec, count } = revokeSource(prefillSpec(), 'sibling');
  assert.equal(count, 1);
  const card = spec.tracks.find((item) => item.elementId === 'card-1').clips[0];
  assert.equal(card.trigger, 'onEnter');
  assert.equal(card.startMs, 0, '起点必须归零：留 280 会让时间轴色块停在与实际不符的位置');
  assert.equal('triggerSource' in card, false, '来源标注必须清掉，否则它还会被算进「同块带出」');
  assert.equal('confidence' in card, false);
  // 撤销的是绑定，不是动效：clip 还在，用户能再选段号。
  assert.equal(spec.tracks.find((item) => item.elementId === 'card-1').clips.length, 1);
  // 其它来源一条不动。
  assert.equal(spec.tracks.find((item) => item.elementId === 'bullet-1').clips[0].trigger, 'withSegment:seg-2');
  assert.equal(spec.tracks.find((item) => item.elementId === 'bullet-1').clips[0].triggerSource, 'text');
});

test('撤销单条只动那一条，且不动同轨道的强调 clip', () => {
  const { spec, count } = revokeBinding(prefillSpec(), 'bullet-2');
  assert.equal(count, 1);
  const clips = spec.tracks.find((item) => item.elementId === 'bullet-2').clips;
  assert.equal(clips[0].trigger, 'onEnter');
  assert.equal('triggerSource' in clips[0], false);
  assert.equal(clips[1].trigger, 'atTime:900', '强调 clip 不该被撤销波及');
});

test('撤销不存在的来源是空操作（不产生新对象）', () => {
  const spec = prefillSpec();
  const result = revokeSource(spec, 'structural');
  assert.equal(result.count, 0);
  assert.equal(result.spec, spec, '没改动就该返回原对象：否则会被当成一次编辑压进撤销栈');
});

test('撤销之后草案必须算脏（否则保存按钮还是灰的，撤销无声失败）', () => {
  const spec = prefillSpec();
  const { spec: revoked } = revokeSource(spec, 'sibling');
  assert.equal(isDirty(revoked, spec), true, '撤销改了 trigger 与来源，必须能被脏判据看见');
  assert.equal(isDirty(draftOf(revoked), revoked), false);
});

test('保存载荷带回来源与置信度（不回传等于保存一次就抹掉）', () => {
  const payload = savePayloadOf(prefillSpec());
  const bullet = payload.tracks.find((item) => item.elementId === 'bullet-1').clips[0];
  assert.equal(bullet.triggerSource, 'text');
  assert.equal(bullet.confidence, 0.42);
  const card = payload.tracks.find((item) => item.elementId === 'card-1').clips[0];
  assert.equal(card.triggerSource, 'sibling');
  // undefined 而不是 0：JSON.stringify 会丢掉它，服务端保持「这条没有置信度」，
  // 而不是收到一个"相似度 0 分"的结论。
  assert.equal(card.confidence, undefined, '没有置信度就不要写一个 0 进载荷');
});

test('未保存的第一份规格算脏：新建页面必须存得下去', () => {
  // 曾经 isDirty(draft, null) = false：选了预设 → 保存按钮一直是灰的，预览也静默什么都不做。
  assert.equal(isDirty(prefillSpec(), null), true);
  assert.equal(isDirty(null, null), false, '没有草案不是编辑');
});

test('换风格搬走入场时机与来源，但不搬 atTime', () => {
  const generated = {
    ...prefillSpec(),
    presetId: 'tech-dynamic',
    tracks: [
      { elementId: 'bullet-1', kind: 'bullet', clips: [{ id: 'n1', type: 'slideIn', startMs: 0, durationMs: 600, easing: 'easeOut', trigger: 'onEnter', params: { direction: 'up' } }] },
      { elementId: 'bullet-2', kind: 'bullet', clips: [{ id: 'n2', type: 'slideIn', startMs: 0, durationMs: 600, easing: 'easeOut', trigger: 'onEnter' }] },
      { elementId: 'new-1', kind: 'bullet', clips: [{ id: 'n3', type: 'fadeIn', startMs: 0, durationMs: 400, easing: 'easeOut', trigger: 'onEnter' }] }
    ]
  };
  const carried = carryBindings(prefillSpec(), generated);
  const bullet = carried.tracks.find((item) => item.elementId === 'bullet-1').clips[0];
  assert.equal(bullet.trigger, 'withSegment:seg-2', '换风格不该把复核过的绑定退回页首');
  assert.equal(bullet.triggerSource, 'text');
  assert.equal(bullet.confidence, 0.42);
  assert.deepEqual(bullet.params, { direction: 'up' }, '动效本身必须用新预设的');
  // 原规格里 bullet-2 的入场挂了 atTime 之外的强调；入场是 withSegment（搬），强调不是入场（不搬）。
  const emphasis = carried.tracks.find((item) => item.elementId === 'bullet-2').clips.find((clip) => clip.type === 'highlight');
  assert.equal(emphasis, undefined, '新预设没有这条强调 clip，不该无中生有');
  // 新预设独有、旧草案没有的元素保持原样。
  assert.equal(carried.tracks.find((item) => item.elementId === 'new-1').clips[0].trigger, 'onEnter');
});

test('默认规格查询：只有显式要求才带 prefill', () => {
  assert.deepEqual(defaultSpecQuery('business-calm', false), { default: 'true', preset: 'business-calm' });
  assert.deepEqual(defaultSpecQuery('business-calm', true), { default: 'true', preset: 'business-calm', prefill: 'true' });
  // 预设为空时交给服务端默认，但 prefill 开关必须照发。
  assert.deepEqual(defaultSpecQuery('', true), { default: 'true', prefill: 'true' });
});

test('人手改段号后清掉来源：复核表当场更新，而不是等保存后才变', () => {
  const changed = withTrackTrigger(prefillSpec(), 'bullet-1', segmentTrigger('seg-4'));
  const clip = changed.tracks.find((item) => item.elementId === 'bullet-1').clips[0];
  assert.equal(clip.trigger, 'withSegment:seg-4');
  assert.equal('triggerSource' in clip, false, '这条结论不再来自规则 text');
  assert.equal(prefillBindings(changed).some((item) => item.elementId === 'bullet-1'), false);
});

test('未登记的来源不硬套文案', () => {
  assert.equal(prefillSourceKey('sibling'), 'editor.animationSourceSibling');
  assert.equal(prefillSourceKey('layout-v2'), '', '服务端加了新规则时宁可显示原始 id，也不要错配到别的文案');
  assert.deepEqual(filterBySource(prefillBindings(prefillSpec()), 'text').map((item) => item.elementId), ['bullet-1', 'bullet-2']);
});

test('低置信提示按「缺的是哪一侧」分叉', async () => {
  assert.equal(prefillLowConfidenceKey('script'), 'editor.animationPrefillLowConfidenceScript');
  assert.equal(prefillLowConfidenceKey('layout'), 'editor.animationPrefillLowConfidenceLayout');
  assert.equal(prefillLowConfidenceKey('both'), 'editor.animationPrefillLowConfidenceBoth');
  // 未登记的 reason 退回版面那句，不凭空造 key。
  assert.equal(prefillLowConfidenceKey(undefined), 'editor.animationPrefillLowConfidenceLayout');
  assert.equal(prefillLowConfidenceKey('layout-v3'), 'editor.animationPrefillLowConfidenceLayout');
  // no-script 不能退回版面那句：一页有编号有卡片的版面被判 low-confidence/no-script 时，
  // 「没有检测到结构化版面信号」是假话，而且它把用户引去重排 PPT——真正缺的是讲稿。
  assert.equal(prefillLowConfidenceKey('no-script'), 'editor.animationPrefillLowConfidenceNoScript');

  // script 档的版面是有信号的：文案必须点名「讲稿」，否则用户会去重排 PPT。
  const zh = JSON.parse(await readFile(new URL('./src/i18n/zh.json', import.meta.url), 'utf8'));
  const en = JSON.parse(await readFile(new URL('./src/i18n/en.json', import.meta.url), 'utf8'));
  const zhScript = zh[prefillLowConfidenceKey('script')];
  const enScript = en[prefillLowConfidenceKey('script')];
  assert.ok(zhScript.includes('讲稿'), `script 档文案没说清该改讲稿：${zhScript}`);
  assert.ok(!zhScript.includes('没有检测到结构化版面信号'), '版面明明有信号，不能复用版面文案');
  assert.ok(enScript.toLowerCase().includes('narration'), `英文 script 档文案未点名讲稿：${enScript}`);
  // 四份文案必须各不相同：合成一句就等于不分档。
  const reasons = ['layout', 'script', 'both', 'no-script'];
  const texts = reasons.map((r) => zh[prefillLowConfidenceKey(r)]);
  assert.equal(new Set(texts).size, 4, '低置信四种原因的文案重复了');
  texts.forEach((text, i) => assert.ok(text, `${reasons[i]} 档缺中文文案`));
  reasons.forEach((r) => assert.ok(en[prefillLowConfidenceKey(r)], `${r} 档缺英文文案`));
  // no-script 指向的补救动作是「先写稿」，不能是「改讲解词」。
  const zhNoScript = zh[prefillLowConfidenceKey('no-script')];
  assert.ok(zhNoScript.includes('写'), `no-script 档没说该先写稿：${zhNoScript}`);
  assert.ok(!zhNoScript.includes('要提升就先改讲解词'), 'no-script 不该让人去改还不存在的讲稿');
});

test('讲稿侧信号有文案，未登记信号不硬套', () => {
  assert.equal(prefillSignalKey('script-structure'), 'editor.animationSignalScriptStructure');
  assert.equal(prefillSignalKey('script-flat'), 'editor.animationSignalScriptFlat');
  assert.equal(prefillSignalKey('script-absent'), 'editor.animationSignalScriptAbsent');
  assert.equal(prefillSignalKey('script-v2'), '');
});

test('模型来源：有文案 key，且复核顺序紧跟同块带出', () => {
  // 模型绑定是唯一「证据来自模型自述」的来源，实测在 script 档 0.833——比结构化页的
  // 规则（0.972）低，且模型看不见容器（四步流程页实测漏 9/18）。漏绑比错绑更难发现，
  // 所以它必须排在只靠几何推断的规则之前。
  assert.equal(prefillSourceKey('model'), 'editor.animationSourceModel');
  const spec = prefillSpec();
  const modelTrack = spec.tracks.find((item) => item.elementId === 'bullet-2');
  modelTrack.clips[0].triggerSource = 'model';
  modelTrack.clips[0].confidence = 0.42;
  const order = reviewOrder(prefillBindings(spec)).map((item) => `${item.source}:${item.elementId}`);
  assert.deepEqual(order, ['sibling:card-1', 'model:bullet-2', 'prior:title-1', 'text:bullet-1']);
  // 模型自报的把握要如实带出来：它是复核排序与面板显示的唯一依据。
  const model = prefillBindings(spec).find((item) => item.source === 'model');
  assert.equal(model.hasConfidence, true);
  assert.equal(model.confidence, 0.42);
});

test('模型没用上的原因各自有文案，未登记的原因不硬套', () => {
  assert.equal(prefillModelFallbackKey('timeout'), 'editor.animationPrefillReasonTimeout');
  assert.equal(prefillModelFallbackKey('quota'), 'editor.animationPrefillReasonQuota');
  assert.equal(prefillModelFallbackKey('no_valid_bindings'), 'editor.animationPrefillReasonNoBindings');
  // 没有原因（模型正常产出或这一页本就不该问模型）就不该有任何提示。
  assert.equal(prefillModelFallbackKey(''), '');
  assert.equal(prefillModelFallbackKey(undefined), '');
  // 服务端新增原因时显示原文，不要错配成 timeout 这类会误导下一步动作的文案。
  assert.equal(prefillModelFallbackKey('rate_limited'), '');
});

test('等待预算：超时才放弃，没开始等不算超时', () => {
  // 服务端单页模型超时 45s。预算必须明显大于它，否则「模型正常但慢」会被当成
  // 「任务没被领取」，用户看到的提示会指向错误的排查方向。
  assert.ok(PREFILL_WAIT_BUDGET_MS > 45_000, `预算 ${PREFILL_WAIT_BUDGET_MS}ms 没有给模型留出时间`);

  const start = 1_000_000;
  assert.equal(prefillWaitExpired(start, start + 1_000), false);
  assert.equal(prefillWaitExpired(start, start + PREFILL_WAIT_BUDGET_MS - 1), false);
  // 边界取 >=：正好到点就该放弃，不然「差一毫秒」会再来一轮。
  assert.equal(prefillWaitExpired(start, start + PREFILL_WAIT_BUDGET_MS), true);
  assert.equal(prefillWaitExpired(start, start + PREFILL_WAIT_BUDGET_MS + 60_000), true);

  // 没有在等（0 / 非法值）时永远不算超时：首帧或已复位后误判会把刚发出的任务立刻放弃。
  assert.equal(prefillWaitExpired(0, start), false);
  assert.equal(prefillWaitExpired(Number.NaN, start), false);
  assert.equal(prefillWaitExpired(start, Number.NaN), false);

  // 预算可覆盖：用例自己指定更小的值，避免测试跟着产品常量一起漂。
  assert.equal(prefillWaitExpired(start, start + 5_000, 5_000), true);
});

test('等待超时有自己的文案，不并进服务端 timeout', () => {
  // 两者排查方向相反：服务端 timeout 是模型自己慢（调超时/换模型），
  // client_timeout 是结果压根没回来（查 worker 有没有起、任务有没有被领取）。
  assert.equal(prefillModelFallbackKey('client_timeout'), 'editor.animationPrefillReasonClientTimeout');
  assert.notEqual(
    prefillModelFallbackKey('client_timeout'),
    prefillModelFallbackKey('timeout')
  );
});

test('buildLayerNodes：矩形焦点生成遮罩节点，定位与强度都对', () => {
  const created = [];
  // 最小 DOM stub：只需要 createElement 的 style/append/animate。
  globalThis.document = {
    createElement(tag) {
      const el = {
        tag,
        style: {},
        children: [],
        append(child) { this.children.push(child); },
        animate(frames, opts) {
          this.frames = frames;
          this.timing = opts;
          return { pause() {}, currentTime: 0 };
        }
      };
      created.push(el);
      return el;
    }
  };
  const container = { children: [], append(el) { this.children.push(el); }, animate() {} };
  const focus = [{
    startMs: 1000,
    elementId: 'p1-s2-b0',
    endMs: 3000,
    fadeMs: 240,
    rect: { x: 385, y: 384, w: 173, h: 415 }
  }];
  const out = buildLayerNodes(container, [], 1920, 1080,
    { pageDurationMs: 4000, fps: 30, focus });
  assert.equal(out.length, 1, '一段矩形焦点应生成一个遮罩节点');
  const style = out[0].element.style;
  // 这一条就是那次 bug：没有定位，left/top 被静默忽略，白纱从左上角铺满全页。
  assert.equal(style.position, 'absolute', '遮罩节点必须定位，否则 left/top 无效');
  const near = (got, want) => Math.abs(parseFloat(got) - want) < 0.01;
  assert.ok(near(style.left, (385 / 1920) * 100), `left=${style.left}`);
  assert.ok(near(style.top, (384 / 1080) * 100), `top=${style.top}`);
  assert.ok(near(style.width, (173 / 1920) * 100), `width=${style.width}`);
  assert.ok(near(style.height, (415 / 1080) * 100), `height=${style.height}`);
  assert.equal(style.opacity, '0', '初始不压暗：没有动画时只是少一层遮罩');

  const frames = out[0].element.frames ?? [];
  assert.ok(frames.length >= 2, '应当有关键帧');
  // 1240–3000ms 是 hold（1000ms 起、240ms 淡入之后到放手之前）：这段时间必须稳定
  // 在 0.45。窗口按毫秒给而不是按 offset 粗略卡：过渡窗口现在按 8ms 加密采样，
  // 拿 offset>0.3 当界会把淡入尾巴（0.448…）也算进 hold。
  const hold = frames.filter((f) => {
    const ms = Number(f.offset) * 4000;
    return ms >= 1240 && ms <= 3000;
  });
  assert.ok(hold.length >= 2);
  for (const f of hold) {
    assert.ok(Math.abs(Number(f.opacity) - 0.45) < 1e-9, `hold 期 opacity=${f.opacity}`);
  }
  assert.equal(Number(frames[0].opacity), 0, '段首之前不压暗');
  delete globalThis.document;
});

test('needsLayerStage：只有矩形焦点、没有层的页也要建舞台', () => {
  // 这是我们踩过的第三种「同一份清单、两种画面」：清单里带 focus.rect、layers 为空，
  // 播放器按 layers.length 提前 return，遮罩节点根本没建——MP4 在指，网页版全亮。
  const rectFocus = [{ startMs: 1000, elementId: 'p1-s2-b0', rect: { x: 10, y: 20, w: 30, h: 40 } }];
  assert.equal(needsLayerStage([], rectFocus), true, '有矩形焦点就必须建舞台');
  assert.equal(needsLayerStage([{ box: { x: 0, y: 0, w: 10, h: 10 }, image: 'a.png' }], []), true,
    '有层当然要建');
  assert.equal(needsLayerStage([], []), false, '什么都没有就不建');
  // 图层压暗型焦点（没有 rect）：没有层就没有可压暗的对象，建了也是空舞台。
  assert.equal(needsLayerStage([], [{ startMs: 0, elementId: 'title-1' }]), false);
  // 空矩形不算：buildFocusMasks 也会跳过它，提前建舞台只会多一个空 div。
  assert.equal(needsLayerStage([], [{ startMs: 0, elementId: 'x', rect: { x: 1, y: 2, w: 0, h: 4 } }]), false);
  assert.equal(needsLayerStage([], undefined), false);
});

test('pickStageLayers：低电量也只省元素层，焦点遮罩照建', async () => {
  // 低内存设备（deviceMemory < 4）此前整页不建舞台，焦点遮罩跟着一起没了：
  // 成片和高端机在指，低端机全亮，两端都不报错。降级的本意是省合成层与图片解码，
  // 而遮罩只是几个 div —— 该省的是元素层。
  const { pickStageLayers } = await import('./src/sharePlayer/animation.ts');
  const rectFocus = [{ startMs: 1000, elementId: 'p1-s2-b0', rect: { x: 10, y: 20, w: 30, h: 40 } }];
  const oneLayer = [{ box: { x: 0, y: 0, w: 10, h: 10 }, image: 'a.png' }];

  // 正常设备：层一个不少。
  assert.deepEqual(pickStageLayers(oneLayer, [], false), oneLayer);
  assert.deepEqual(pickStageLayers(oneLayer, rectFocus, false), oneLayer);

  // 低电量 + 只有层：没有可看的焦点，舞台上什么都不剩 → 不建。
  assert.equal(pickStageLayers(oneLayer, [], true), null);
  // 低电量 + 有矩形焦点：舞台要建，但元素层一个不放（遮罩由 buildLayerNodes 追加）。
  assert.deepEqual(pickStageLayers(oneLayer, rectFocus, true), []);
  assert.deepEqual(pickStageLayers([], rectFocus, true), []);
  // 低电量 + 只有「图层压暗」型焦点（无 rect）：没有层可压暗、也没有遮罩 → 不建。
  assert.equal(pickStageLayers(oneLayer, [{ startMs: 0, elementId: 'title-1' }], true), null);
  // 什么都没有：两档都不建。
  assert.equal(pickStageLayers([], [], true), null);
  assert.equal(pickStageLayers([], [], false), null);
});

// ---- 焦点过渡：网页版与成片在**每个帧时刻**逐点一致 ----
//
// 两端口径写的是「同一个函数」（focusMask / focusDim 来自 shared/motion.ts），
// 但网页版把结果编译成 WAAPI 关键帧、帧与帧之间线性插值。只把关键帧放在过渡的
// 两个端点上，等于用一条弦代替 easeOutCubic：240ms 过渡的中点，成片遮罩已经压到
// 0.394，网页版还停在 0.225。两端都不会报错，并排播放才看得出「网页版淡得慢」。
// 真浏览器先量到了这个数（tmp/lowpower-probe.mjs 在 1060/1120/1180/1240ms 读到
// 0.1125/0.225/0.3375/0.45，一条完美的直线），这里把它钉成 CI 判据。

/** 按 WAAPI 的方式把关键帧还原成「某一毫秒的实际值」：offset 之间线性插值。 */
const sampleKeyframes = (frames, pageMs, t) => {
  const at = (f) => ({ ms: f.offset * pageMs, v: Number(f.opacity) });
  const pts = frames.map(at);
  if (t <= pts[0].ms) return pts[0].v;
  for (let i = 1; i < pts.length; i++) {
    if (t <= pts[i].ms) {
      const a = pts[i - 1];
      const b = pts[i];
      const span = b.ms - a.ms;
      return span <= 0 ? b.v : a.v + ((b.v - a.v) * (t - a.ms)) / span;
    }
  }
  return pts[pts.length - 1].v;
};

test('矩形焦点：过渡区间里网页版也走曲线，不是一条弦', async () => {
  const { focusMaskKeyframes } = await import('./src/sharePlayer/animation.ts');
  const { focusMask } = await import('../shared/motion.ts');
  const pageMs = 6000;
  const rect = { x: 400, y: 300, w: 600, h: 300 };
  // 两个窗口都要走一遍：淡入（起点之后）与放手（endMs 之后）。只补前者，讲完最后
  // 一句时那 240ms 的「放回去」仍然是一条斜线。
  const cases = [
    { name: '淡入', span: { startMs: 1000, elementId: 'chart#b0', rect } },
    { name: '放手', span: { startMs: 1000, endMs: 3000, elementId: 'chart#b0', rect } }
  ];
  for (const c of cases) {
    const focus = [c.span];
    const frames = focusMaskKeyframes(focus, c.span, pageMs);
    assert.ok(frames && frames.length > 10, `${c.name}：过渡窗口没有加密采样（${frames?.length} 个关键帧）`);

    let worst = 0;
    let worstAt = 0;
    for (let t = 0; t <= pageMs; t += 1) {
      const exact = focusMask(focus, t)?.alpha ?? 0;
      const diff = Math.abs(sampleKeyframes(frames, pageMs, t) - exact);
      if (diff > worst) {
        worst = diff;
        worstAt = t;
      }
    }
    assert.ok(
      worst <= 0.002,
      `${c.name}：网页版与成片的最大差 ${worst.toFixed(4)}（${worstAt}ms）：关键帧只取了端点，线性插值把 easeOutCubic 拉直了`
    );
  }
  // 中点必须落在曲线上（0.45 × easeOutCubic(0.5) = 0.39375），线性插值给出的是 0.225。
  const span = cases[0].span;
  const frames = focusMaskKeyframes([span], span, pageMs);
  assert.ok(Math.abs(sampleKeyframes(frames, pageMs, 1120) - 0.394) < 0.005);
  // 放手那一侧同理：3000ms 之后 240ms 才回到全亮，中点是 0.225 而不是 0.394 的一半。
  const rel = { startMs: 1000, endMs: 3000, elementId: 'chart#b0', rect };
  const relFrames = focusMaskKeyframes([rel], rel, pageMs);
  assert.ok(Math.abs(sampleKeyframes(relFrames, pageMs, 3120) - 0.056) < 0.005,
    `放手中点应为 0.056（0.45 × (1 − easeOutCubic(0.5))），实际 ${sampleKeyframes(relFrames, pageMs, 3120)}`);
});

test('图层让位：过渡窗口同样加密采样，帧时刻的不透明度等于成片', async () => {
  const { compileKeyframes } = await import('./src/sharePlayer/animation.ts');
  const { evaluateLayer, focusDim, focusOpacity } = await import('../shared/motion.ts');
  // 两个图层型焦点（无 rect）在 4s 换人：a 从「亮」退回，b 从「暗」亮起来。
  // 换人点是 4000ms，落在两条 clip 的活动区间（0–600ms）之外——正是「只按 clip
  // 区间采样」会把 240ms 的过渡抹成一整段斜线的那种页。
  const focus = [
    { startMs: 1000, elementId: 'a' },
    { startMs: 4000, elementId: 'b' }
  ];
  const clip = slideInClip(0, 600);
  const pageMs = 6000;
  const opts = { fps: 30, pageDurationMs: pageMs, reduceMotion: false, focus };
  const frames = compileKeyframes([clip], opts, 'b');
  assert.ok(frames);
  const layer = { kind: 'text', assetMode: 'bitmap-region', box: { x: 0, y: 0, w: 0, h: 0 }, clips: [clip] };

  let worst = 0;
  let worstAt = 0;
  for (let t = 0; t <= pageMs; t += 1000 / 30) {
    const exact = evaluateLayer(layer, t).opacity * focusOpacity(focusDim(focus, t)['b'] ?? 0);
    const diff = Math.abs(sampleKeyframes(frames, pageMs, t) - exact);
    if (diff > worst) {
      worst = diff;
      worstAt = t;
    }
  }
  assert.ok(
    worst <= 0.002,
    `图层不透明度与成片的最大差 ${worst.toFixed(4)}（${worstAt.toFixed(0)}ms）`
  );
  // 换人那一刻 b 还在压暗态附近，240ms 后才全亮；线性插值会在中点给出 0.775。
  assert.ok(Math.abs(sampleKeyframes(frames, pageMs, 4120) - 0.944) < 0.01,
    `换人中点应为 0.944（0.55 + 0.45 × easeOutCubic(0.5)），实际 ${sampleKeyframes(frames, pageMs, 4120)}`);
});
