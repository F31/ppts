# 提案：HTML 成品包的 DOM 关键帧（M6-2）

## 为什么做

M6-1 的成品包（`html_bundle`）目前是**有声幻灯**：每页一张 JPEG 底图 + 一条时间轴，切页靠 `startUs` 比对。它已经能解压双击播放，但和 MP4 版不是同一个东西——**MP4 有元素级入场，HTML 没有**。

这不是一个「锦上添花」的缺口，它是同一次导出的两副面孔：用户点「导出视频」拿到会动的片子，点「导出 HTML」拿到不动的，第一反应是「HTML 版坏了」。方案 §20.3 把这条路定成主路径，理由也很硬：矢量清晰、体积小一个数量级、文本可选可搜。缺动画层，这些优势一个都兑现不了。

M2 已经把前提铺完了：`CompiledTimeline` IR 目标无关，`plate.BuildPlate` / `plate.BuildLayerAssets` 已在渲染侧解掉「底图烘焙了元素」这个致命问题（ADR-6/ADR-9）。**本变更要做的不是再解释一遍规格，而是把同一份 IR 的第二个消费端接上**。

## 目标

1. **打包期带上分层资源**：对每页底图跑一遍 `plate.BuildPlate` 与 `BuildLayerAssets`（纯 Go 图像处理，不启动 Chrome），产出「挖空底板 + 每层独立位图」，连同 IR 的 `layers[*].box/clips` 一起写进包。
2. **清单升到 v2**：新增 `animations` 段（逐页 layers/clips/transition），保持旧播放器可忽略未知字段；v1 清单在新播放器里等价于「没有动画」，行为与今天一致。
3. **DOM 侧用 WAAPI 回放**：播放器首次进入某页时，用与 Remotion 同一份 `motion.ts` 把 IR 的 clips 编译成 WAAPI keyframes（含多 clip 重叠的采样合并），之后的回放只做 `play/pause/currentTime`，不做逐帧求值。
4. **主时钟跟音频 `currentTime`**：动画从音频派生，不自己 `requestAnimationFrame` 计步——否则 5 分钟讲解答必然与音轨漂移（§20.5）。
5. **两端同帧 SSIM ≥ 0.95**：这是 M6-2 唯一的硬门禁，也是最容易糊弄过去的一条（只比首帧、只比静态页都能过）。门禁必须逐页取**动画进行中**的帧。

## 非目标

- **不做 catalog `targets`/`domFallback` 的完整落地**。本变更只实现 IR 已能表达的能力（六种属性 + 三种转场），`targets` 字段与降级提示留到 `html-bundle-quality`（M6-4）——现在没有「frames-only 的动效」需要降级，先建机制等于给不存在的调用方造抽象。
- **不做成品级 token / 在线分享**（M6-3），本包仍是本地文件。
- **不做字体子集与 WebP**（M6-4）。分层位图的编码格式本变更取「够用且可测」的保守解，体积优化留给 M6-4。
- **不改 MP4 通道**。IR 语义一行不动，`planHash` 不变；本变更只增加一个消费者。

## 与既有变更的关系

- 依赖 `animation-render-mvp`（M2）的 IR 与 `plate` 包：本变更**复用** `BuildPlate`/`BuildLayerAssets`，不复制一份图像处理逻辑。
- 依赖 `html-share-bundle-static`（M6-1）的打包器与播放器骨架：`BundleInput` 增字段、清单升版本、`main.ts` 增动画层，均向后兼容。
- 依赖 `animation-export-e2e`（M3）的动画开关与规格读取：**开关关闭时包内 `animations` 必须为空**，与「没有这个功能」等价。
- 解开 `animation-render-service-image` 的 **2.9**（两端共享插值）：插值与缓动语义在本变更冻结为「同一份 `renderer/src/motion.ts` 数学定义」，DOM 端通过 WAAPI 的 cubic-bezier 复用同一定义，而不是各写一套求解器。

## 关键取舍（细节见 design.md）

**DOM 端只能靠分层位图，不能靠整页变换，也不能靠文字重绘。**

三个选项都核过：整页变换做不到元素级时序（SSIM 必然远低于 0.95）；用文字重绘需要文本内容与字号颜色，而 IR 里 `Layer` 只有 `Box`——没有可绘制的文字，重绘出来是空白矩形。分层位图是唯一能让两端像素对齐的路径，且它已经在 `plate.go` 里为 Remotion 侧实现过一遍。

**代价是包体积**。分层位图是一页多张的增量，文字层的 JPEG 会糊边，所以文字层用 PNG。这条会写进体积基线，M6-4 再做子集化与 WebP。
