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

## 阶段一增强（2026-09-24，P2）与实现细节

- **VAD 更敏感**：`vadMinSilenceMS` 90ms → **55ms**，捕获短句/从句间更短的换气停顿，增加锚点、
  缩短"匀速区间"，从而减小长从句内部漂移。
- **区间内按字权重分布**：`distributeByAnchors` 由"字符等分"改为 `allocateWeighted` —— 按
  CJK(1.0) > 数字(0.6) > 拉丁(0.55) > 空白(0.3) > 标点(0.35) 的权重分配时长，改善中英混排/
  标点场景的字级贴合（每个字符 ≥1µs，区间落点精确）。
- 合成基准（受控语料，非人工听音）：`estimated` MAE 184ms → `estimated_vad` **MAE ≈ 20ms**。
  真实 CosyVoice2 语音提升幅度会小于此值，方向一致。

## 显示文本一致性（P4）

时间轴的 `SubtitleCue.Text` 是**显示文本**，而字级时间戳来自**朗读文本**（可能经发音词典
替换）。二者字数不同时会造成高亮下标错位。`media.BuildTimeline` 现按"边界时间插值"把
M 个字级时间戳重映射到 N 个显示字符（`remapCharsToCount`），保证字数一致、时间单调、覆盖
原区间。若需根治，仍建议阶段二强制对齐直接输出显示文本的字级时间戳。

## 用户侧微调（P3，播放器）

播放器新增「同步」控件：±50ms 微调高亮相对音频的偏移（正=高亮延后），按 `timelineKey`
持久化在本机，用于消除残余的系统性偏移。

## 音节核锚定（2026-09-26，不含本地模型的算法强化）

残余不同步的根源：静音锚点之间仍是"按字权重匀速"分布，而真实音节的时长并不均匀。
**不引入本地/远程模型**，仅用合成音频本身就能大幅压缩误差——检测每个音节的能量核
（帧 RMS + 平滑 + 局部峰 + 峰前谷底），把字符边界锚到**真实音节起始**：

- 只依赖 PCM16 WAV 的能量包络，无模型依赖；核数不足时按块回退"按权重匀速"。
- 块归属窗口改用**停顿中点**切分，避免 VAD 停顿端点的帧量化把下一块首音节裁掉
  （否则整块后移一个音节，误差达 200ms+）。
- 音节核起点回退在"有声地板"处止步，防止穿过停顿串到前一块。
- 无内部停顿的长从句（无标点、无换气）同样适用：整段走音节核，正是旧方法会退化成
  盲估的场景。

量化对比（合成音节包络语料，真值=逐字起始时刻；363 字）：

| 方法 | MAE(ms) | MaxAE(ms) |
| --- | --- | --- |
| estimated（纯盲估） | 184.2 | 371.6 |
| estimated_vad（静音锚点+匀速，P2） | 45.3 | 368.7 |
| estimated_vad（音节核锚定，当前） | **20.2** | **33.6** |

注意：实测改善取决于真实 TTS 音节能量是否清晰；若某块检测不可靠会自动退回升级前的
匀速分布，因此只会更好、不会更差。该能力通过 `alignmentCacheVersion=3` 对既有缓存
生效（重新生成配音即自动"仅重算对齐"，不重新合成）。

## 停顿锚点修复与真实音频校验（同日跟进）

真实 CosyVoice2 分段（seg-01，261 字 / 46.7s）复现优化后暴露旧 `mapPunctToSilence` 的
**等比抽样盲配**：标点与停顿数量不等时会把停顿错配到无关标点，产出"单个字吃 900ms""整个
短语被压成每字 10ms"的畸形块，正是用户感知的明显错位。修复：

- 停顿锚定改为 **DP 单调最优匹配**：按字数线性预估"标点应到时刻"与静音中点的归一化距离，
  距离过远的不配（宁可整段回退匀速，也不要错切块）；
- 顿号/冒号（`、` `：` `:`——TTS 口语不断句）移除出停顿标点集，不再把顺读列举短语切块；
- 句读标点时长权重 0.35→0.05（几乎不占朗读时间，把时间还给实词）；
- 最短停顿时长 55→40ms，捕获更短的换气停顿。

真实音频校验（同一 seg-01，无人工听音，算法度量）：

| 指标 | 线上缓存（P2） | 修复后 |
| --- | --- | --- |
| 逐字时长 max | 1463ms | 306ms |
| 逐字时长 CV | 1.06 | 0.34 |
| 逐字时长中位数 | 138ms | 178ms（≈真实语速） |

合成语料对照（更新后）：盲估 184.2ms → 静音锚点+匀速 40.1ms → 音节核锚定 **20.2ms**。
音节核在 CosyVoice 上常欠检出（seg-01 仅 110/261），此时自动按块匀速兜底，不会变差。
