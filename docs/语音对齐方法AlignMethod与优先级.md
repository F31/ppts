# 语音对齐方法（AlignMethod）与优先级

本文说明当前系统支持的字级时间戳生成方法、优先级与降级链，以及"网页高亮 / MP4 字幕烧录"
同源的保证。

## 背景

播放器（网页高亮）与导出 MP4（`media.RenderASS` 字幕烧录）都消费同一个来源：
`media.Timeline.Subtitles[].Chars`。该字段由 `media.BuildTimeline` 从每段的
`SegmentInput.Alignment.Tokens` 偏移到全局时钟得到。因此**对齐精度的修复只需发生在
"生成 alignment"这一层**，网页与 MP4 自动同时受益，无需分别改播放器与字幕渲染。

对齐来源由 `tts.Alignment.Method`（内部 `tts.AlignmentMethod`）标注。

## 支持的方法

| 方法（常量 / 值） | 含义 | 生成位置 | 状态 |
|---|---|---|---|
| `AlignProvider` / `provider_timestamps` | 供应商原生字/词时间戳（如 Azure boundary events） | 供应商适配器 | 预留（当前 SiliconFlow 不提供） |
| `AlignForced` / `forced_alignment` | 强制对齐（已知文本+音频反推真实字级时间戳） | 独立对齐服务/模型 | **待落地（阶段二）** |
| `AlignEstimateVAD` / `estimated_vad` | 静音锚点估算：VAD 检测真实停顿 → 锚到标点 → 小区间内匀速 | `internal/integrations/tts/vad_align.go` | **已实现（阶段一）** |
| `AlignEstimate` / `estimated` | 整体净时长内匀速估算（无 VAD） | `internal/integrations/tts/siliconflow.go:buildEstimatedAlignment` | 已实现（兜底） |

## 优先级与降级链

生成 alignment 时按下列顺序尝试，任一步失败/不可信则降级到下一步：

```
provider_timestamps ──(供应商提供时间戳时)
        │ 不可用
        ▼
forced_alignment ─────(强制对齐成功且置信度达标；阶段二)
        │ 失败 / 低置信度 / 服务不可用
        ▼
estimated_vad ────────(检测到有效停顿且能锚到标点)
        │ 无有效静音 / 无标点 / 映射后区间非法
        ▼
estimated ────────────(整体匀速，永远可用的兜底)
```

- 阶段一（`estimated_vad`）**不会**因 VAD 失败而报错：`buildEstimatedVADAlignment`
  在无有效静音、无标点、或锚点区间非法时统一回退到 `buildEstimatedAlignment`
  （`AlignEstimate`），保证配音流程不中断。
- 阶段二（`forced_alignment`）落地后，其失败/低置信度也必须回退到 `estimated_vad`
  或 `estimated`，绝不让配音流程报错或阻塞。
- 基线校验 `internal/app/narration.go:validateSynthesisResult` 不区分方法名，只校验
  token 单调、不重叠、且落在音频时长内；新增/使用任何 AlignMethod 都照常通过边界校验
  （已有 `TestValidateSynthesisResultAcceptsAlignMethods` 守护）。

## 阶段一：静音锚点估算（estimated_vad）

实现见 `internal/integrations/tts/vad_align.go`：

1. `wavPCM16` 解析 PCM16 采样；`detectSilenceRuns` 以 20ms 帧做能量 VAD
   （帧 RMS < max(峰值×8%, 绝对下限) 判静音），取出**内部**静音区间（≥90ms），
   并得到首/末语音时刻（lead / speechEnd）。
2. 只把**其后仍有字符**的标点纳入锚点（末尾标点之后的停顿会与音频尾部静音合并，
   锚定反而会错配）；`mapPunctToSilence` 把标点按顺序映射到静音区间（数量不等时按
   比例抽样），停顿保留在锚点处。
3. `distributeByAnchors` 在锚点切出的小区间内匀速分配字符，余量并入区间末字符，
   保证落点精确、单调、不越界。
4. 任一环节不成立 → 回退 `buildEstimatedAlignment`（`AlignEstimate`）。

调参：`vadFrameMS=20`、`vadMinSilenceMS=90`、`vadSilenceRel=0.08`、`vadSilenceAbs=40`。

### 量化对比（合成受控语料）

`TestAlignmentQuantitativeComparison`（`internal/integrations/tts/alignment_bench_test.go`）
以"已知逐字真实起始时刻"的合成语料对比两种方法（真值来自受控合成信号，非人工听音标注；
真实 TTS 的人工标注基准建议作为上线前补充验收）：

| 分组 / 方法 | MAE(ms) | MaxAE(ms) | 字数 |
|---|---|---|---|
| 全部 / estimated（当前） | 184.4 | 371.6 | 309 |
| 全部 / estimated_vad（阶段一） | **83.9** | **182.8** | 309 |
| 常规抖动(±6%) / estimated | 187.3 | 361.7 | 185 |
| 常规抖动(±6%) / estimated_vad | 83.2 | 171.7 | 185 |
| 压力抖动(±16%) / estimated | 180.1 | 371.6 | 124 |
| 压力抖动(±16%) / estimated_vad | 85.0 | 182.8 | 124 |

结论：阶段一 MAE 改进约 **54%**、MaxAE 改进约 **51%**，在常规与压力抖动下表现一致
（改善来自停顿锚定，而非样本选择）。残余偏差主要来自"短语内匀速 vs 真实语速起伏"，
这正是阶段二（强制对齐）要解决的部分。

## 验证"网页高亮 与 MP4 字幕同源"

- `TestBuildTimelineUsesAlignmentAndPreservesSlideOrder`：`SegmentInput.Alignment.Tokens`
  → `SubtitleCue.Chars`（全局偏移）。
- `TestRenderASSUsesTimelineAlignmentChars`：`RenderASS` 直接消费 `timeline.Subtitles`
  的 `Chars` 生成逐行轮换 + 朗读高亮的 ASS。
- `TestRenderASSSplitsLinesAndKaraoke`：ASS 逐行轮换 + `\k` 卡拉OK 高亮。

三者共同保证：playback（前端）与 MP4 烧录读的是同一份时间轴数据，修复一处即两处生效。
