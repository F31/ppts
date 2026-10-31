# 提案：跨场景元素延续（Clip.continues）——让一个元素的动效跨页连续

## 为什么做

设计方案 §19 批次三的 G8 是「跨场景元素延续」：一个元素的动效（如进度条、章节指示器、跟随箭头）要在
相邻页之间连续播放，而不是每页各播各的、换页时整体重置。

当前事实（已核实 `E:/projects/ppts` 仓库真实状态）：
- `internal/animation/model.go:146`：Clip 时间「一律相对本页起点，单位毫秒」。
- `internal/render/compile.go:426-472`：超页尾的 clip 被 `DiagClipTruncated` **截断/丢弃**（今天一个
  想跨页的 clip 会被静默杀掉）。
- `openspec/specs/animation-spec/spec.md:24`：`Track.clips[]` MUST 按页内相对时间表达。
- `openspec/specs/render-plan/spec.md:40`：编译后 `startMs + durationMs ≤ pageDurationMs`。
- 全仓 `internal/animation`、`internal/render/ir.go`、`shared/types.ts` 中**无任何** continuation /
  延续 / 全局时间概念。

已归档的 `animation-camera` 在 OUT 里显式 defer 了 G8，称其为「改 clip 时间基准，两个量级的大重构」。
经可行性调研（Explore 代理 + 手动核实），真正的「两个量级」是**路线 A（全量 deck 全局时间基准）** 的代价；
**路线 B（手术式跨页延续）** 可把 blast radius 压缩到「每页仍是独立段、只是多带一个带标记的层 + 一个时间
偏移」，完全不触动「一页=一缓存单元」(`render-plan:207`)、「按页取样」(`html-share-quality`)、
「转场垫末帧」等写死假设。本提案采用**路线 B**。

## 目标（路线 B）

保留 IR 的页相对存储，**新增「延续」语义**：一个 `Clip` 可声明 `continues: true`，其在编译期被**在页边界
拆分**——本页发 `[起点,页尾]` 的 Motion，次页发 `[0,余量]` 的 Motion（**不重复入场**），播放器按
`element_id` 把次页层与本页同元素层**链接为同一视觉实体**，换页时不重置其动画状态。首轮 MVP 限
「单元素跨相邻两页延续一次」。

## 三条设计取舍（在 `design.md` 落死）

1. **复用 `element_id` 作跨页身份，不引入新 ID 体系**。`animation-spec` 的 `Track.element_id` 已指向
   `project.Shape.ID`；在 PPT 中跨页保留的元素天然共享同一 shape id，正好作延续链接键。
2. **拆分而非改写时间基准**：clip 仍是页内相对 `start_ms/duration_ms`；只有 `continues` 的 clip 在编译期被
   拆成两段 Per-page Motion，次页段带 `continuation_offset_ms`（本页段末尾对应的 clip 时间）与
   `is_continuation` 标记。渲染/播放只多读两个字段，不动全局时间模型，不动片段缓存粒度。
3. **无 `continues` = 当前行为**：`continues` 缺省 false，画面与引入本提案前逐像素一致；旧播放器忽略
   新字段（次页段 `is_continuation` 不被识别时退化为普通层，仅失去续播、不崩）。

## 范围

### IN
- `animation-spec`：`Clip` 增 `continues` 布尔（与既有 `element_id` 配合）；放松「页内相对 / ≤ 页时长」
  的 MUST，允许 `continues` 的 clip 超出页时长、由编译拆分（而非被截断）。
- `animation-track-editing`：编辑器可为 clip 标记 `continues`，并把 clip 拖出本页尾（超出时不再被
  `clampClip` 截断，而是标记为逃逸/延续）。
- `render-plan`：`compileClip` 识别 `continues` 溢出 → 拆成本页段 + 次页段（带 offset、标记
  `is_continuation`）；次页 `element_id` 缺失时记 `continuation_target_missing` 诊断。
- `html-share-bundle`：清单 `SlideLayer.Clips`（Motion）携带 `element_id` + `is_continuation` +
  `continuation_offset_ms`；播放器按 `element_id` 跨页链接同元素层、延续段不重放入场。
- `html-share-quality`：页取样门禁新增「延续」fixture/case，断言页缝处视觉连续（无跳变）。

### OUT（本变更不做，留给后续独立 change）
- **路线 A 全量 deck 全局时间基准**：不改 clip 字段为全局坐标，不动 `media` 拼接 /
  `UnscopedSegmentID` 桥接 / `SegmentOffsets` 换算。
- **链式跨多页（>2 页）延续**：MVP 只做相邻两页一次；多页链式留后续（拆分逻辑可自然扩展为 N 段）。
- **跨页元素的静态位置/样式跨页联动**：MVP 要求作者在两页把同元素放在**相同静态位置**；跨页位置差的
  自动插值留后续。
- **合成器（MP4）侧真正续播**：v1 IR 携带 `continuation_offset_ms`，渲染器未实现时次页段退化为普通层
  （仍可见、仅不续播，不崩）；渲染器侧真正续播留后续 change（属已知降级，非阻断）。

## 验证纪律

合并门禁（每里程碑）：`go build ./...` / `go vet ./...` / `go test ./...`（含编译拆分 Go 单测）、
`tsc -b` / `vite build`（含 `internal/share/assets/app.css` 同步重建）、`npm run build:share` 重建
`app.js`、播放器续播纯函数单测（与 Go 同语义）、`share-verify` / `shots-check.mjs` 含延续 fixture。
一个里程碑 = 一次 commit + push + 全量验证。
