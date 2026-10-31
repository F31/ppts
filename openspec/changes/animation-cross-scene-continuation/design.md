# 设计：跨场景元素延续（Clip.continues）

> 三条设计取舍已拍板：**复用 `element_id` 跨页身份**、**拆分而非改写时间基准**、**无 `continues` =
> 当前行为**。本文件把这三条例成可执行契约，然后才写代码。

## 1. 数据模型

cross-page 延续只新增「一个开关 + 一个时间偏移 + 一个标记」，复用既有 `element_id` 作身份：

```go
// animation/model.go（MODIFY Clip）
type Clip struct {
    Type      string `json:"type"`
    Trigger   string `json:"trigger"`     // onEnter | withSegment:<id> | atTime:<ms>
    StartMs   int64  `json:"startMs"`     // 页内相对毫秒
    DurationMs int64 `json:"durationMs"`
    Easing    string `json:"easing"`
    Continues bool   `json:"continues"`   // NEW：本 clip 允许跨页延续到下一页
    // 跨页身份复用 Track.ElementID（已有字段，指向 project.Shape.ID）
}

// render/ir.go（MODIFY Motion）—— 编译产物
type Motion struct {
    StartMs   int64   `json:"startMs"`
    DurationMs int64  `json:"durationMs"`
    // ... 既有 opacity/translateX/translateY/scale 起点终点 + cubic-bezier ...
    IsContinuation      bool  `json:"isContinuation,omitempty"`      // NEW：次页段标记
    ContinuationOffsetMs int64 `json:"continuationOffsetMs,omitempty"` // NEW：本段 localMs=0 对应的 clip 时间
    ElementID           string `json:"elementId,omitempty"`          // NEW：跨页链接键（复用 Track.ElementID）
}
```

`element_id` 来源：`animation-spec:24` 的 `Track.element_id` 已存在，编译时把它从 Track 透传到该 Track
下每个 `Motion.ElementID`。跨页两页的同一 shape 共享 shape id → 天然链接键。

## 2. 编译期页边界拆分（核心数学）

`compileClip`（compile.go）当前对超页尾 clip 记 `DiagClipTruncated` 并截断。改为：仅当
`!clip.Continues` 时保持旧的截断/丢弃行为；当 `clip.Continues == true` 且 `S + D > D_N`（S=StartMs,
D=DurationMs, D_N=本页时长）时，**拆分**为两段，且**两段 Props 在边界处预拆分**保证视觉连续：

- 边界分数 `f = (D_N - S) / D`（本页段占原 clip 的比例）。
- **本页段（page N）**：`StartMs = S`，`DurationMs = D_N - S`；`IsContinuation=false`，`ElementID`
  透传 `Track.ElementID`；`Props = [from, mid]`，其中 `mid = lerp(from, to, f)`。本页段只演到
  clip 时间 `D_N - S` 处的中态。
- **次页段（page N+1）**：`StartMs = 0`，`DurationMs = S + D - D_N`（余量）；`IsContinuation=true`，
  `ElementID` 透传 `Track.ElementID`；`Props = [mid, to]`；`ContinuationOffsetMs = D_N - S`
  （本页段末态在原 clip 中的时间，仅作全局时间对齐/审计用，非插值连续的必要条件）。

**为什么必须预拆分 Props**：若两段都携带完整 `[from, to]`，次页段在 localMs=0 会回到 `from` 重播，
页缝处从「本页段末态」跳回「起点」——视觉断裂。预拆分使两段在 `mid` 精确衔接：本页段末态
（`To=mid`）== 次页段初态（`From=mid`）。播放器只需按各自 `StartMs/DurationMs` 正常插值即得连续结果，
`ContinuationOffsetMs` 退化为可选的审计/对齐字段（见 §3）。

若次页（`plan.Pages[N+1]`）不存在（已是末页），或次页找不到与 `ElementID` 同名的层/shape，编译器记
`DiagContinuationTargetMissing`（warning 级，不阻断整页），该延续 clip 退化为本页段截断（与旧行为一致，
不崩）。

`S ≥ 0` 假设：MVP 只允许在「起始页」为 clip 标 `continues`，故作者侧 `StartMs ≥ 0` 恒成立；次页段本身
不携带 `continues`（它已是余量）。此实现对任意 `S ≥ 0` 均成立（预拆分 Props 不依赖 §3 所述单分母
`clipTime` 公式）；编辑器 UI 实际仅暴露「本页首帧起、跨到下一页」的 `S=0` 用法。链式多页（>2）留后续：
次页段若其 Track 又标 `continues` 则递归再拆（本实现未做，余量段 `Continues=false`）。

## 3. 播放器续播（HTML 播放器，`internal/share/assets` 的 main.ts）

现状：`rebuildLayers(slide)`（main.ts:285）换页即销毁并重建全部层 DOM，并按层施加**入场动效**；
`syncLayers(us)` 用 `localMs = (us - activeSlideStartUs)/1000`（main.ts:300）seek。

延续所需的最小改动：
1. **延续段不重放入场**：`rebuildLayers` 对 `Motion.IsContinuation == true` 的层，**跳过入场动效**，
   直接以「当前静态视觉」就位（不 fade/scale-in）。
2. **视觉连续由数学保证**：延续段的 `Motion` 评估 `clipTime = localMs + offset`，在 localMs=0 时等于
   本页段末态；只要次页同 `element_id` 层的**静态视觉（形状/位置/样式）与本页一致**（作者责任，MVP
   约定），页缝处视觉即连续。播放器无需额外「搬运状态」逻辑——状态连续是插值函数的连续自变量带来的。
3. **转场无缝**：现有转场把「前一页末帧」垫在新页下交叉（main.ts:441-474）。前一页末帧已含元素于缝态，
   新页首帧（延续段 localMs=0）亦为同态 → 交叉淡入无缝。无需改转场。

旧播放器兼容：`is_continuation` / `continuation_offset_ms` 为可选字段，旧播放器忽略 → 次页段退化成普通层
（localMs 直接当 clipTime），最坏情况是缝处轻微回跳，**不崩、不露白**。

## 4. 合成器（MP4 / renderer-image）最小改动

Remotion `Timeline.tsx` 的 Motion 评估需支持 `continuationOffsetMs`：`clipTime = frameMs + offset`。
若渲染器未实现，次页段退化为普通层（visible，仅缝处不续播）——**已知降级、非阻断**，留后续 change 真正
补齐。HTML 播放器是 v1 主目标。

## 5. 片段缓存与门禁（风险已消解）

- `segkey.go`（渲染-plan:207「片段缓存以页为粒度」）：延续只是给**次页**多带一个带标记的层，页面内容
  变化正确反映在该页缓存键上；页粒度不变，**缓存模型不受影响**。
- `html-share-quality` 页取样门禁（V13/V13b/V18 按页取末帧）：结构不变；仅新增一个「延续」fixture，断言
  页缝处同元素位置/不透明度在容差内连续。
- `validate.go` 的 V3 `RuleTime` / V7 `RuleSegment`：对 `continues` 的 clip 放宽「≤ 页时长」钳制
  （改由编译拆分），其余页内校验不变。

## 6. 风险登记

| # | 风险 | 缓解 |
|---|---|---|
| R1 | 编辑器 `clampClip` 把 clip 截回页内 | G8-① 改 `animationTrack.ts`，`continues` 时放行溢出 |
| R2 | 播放器入场动效导致延续段缝处闪一下 | G8-③ `rebuildLayers` 对 `IsContinuation` 跳过入场 |
| R3 | 渲染器不支持 offset → MP4 缝处回跳 | 已知降级（非阻断）；后续 change 补齐 |
| R4 | 次页同 `element_id` 层缺失 → 续播断 | 编译器 `DiagContinuationTargetMissing` 诊断 + 编辑器提示 |
| R5 | 作者两页静态位置不一致 → 缝处跳变 | MVP 约定「同元素同静态位置」；文档写明，后续做位置插值 |

## 7. 明确不做（留给后续独立 change）

- 路线 A 全量 deck 全局时间基准。
- 链式跨多页（>2）延续。
- 跨页元素静态位置/样式自动插值联动。
- 合成器侧真正续播（v1 仅 IR 携带 offset，渲染器未实现时降级）。
