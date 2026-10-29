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
