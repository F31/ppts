# 提案：渲染服务镜像（Remotion vs 纯 ffmpeg 的选型与运行时契约）

## 为什么做

`animation-render-mvp`（M2）把服务契约、IR、客户端都写完了，但只有 stub backend——它写的 `.mp4.stub` 里是文本。**M2 唯一没验收的那条（6.6「30s 样片」）不是缺代码，是缺一个能出像素的运行时**。本机没有 ffmpeg、没有 Chromium 渲染链，路线图上就把这一格空着。

空着会带来两个具体后果：

1. M3（`export.go` 接线）无法判断「动画片段与音频、字幕能不能拼在一起」——拼接要求所有片段同分辨率/同 fps/同像素格式/同 GOP，这些参数只能由真实后端定，不能由 stub 假装。
2. 「L1 元素区域动画到底长什么样」从来没被验证过。**本轮做验证时发现它有一个会毁掉全部入场的缺陷**（见 design.md ADR-6：底图已烘焙元素，抬升后原位置留残影，fadeIn 视觉无效）。这个缺陷在纸面上看不出来，只有真跑一遍才暴露。

所以本变更要交付的不是「一个 Dockerfile」，而是**一套可执行的渲染运行时 + 它的验收方式**：选型有据、镜像可复现、失败可降级、样片可核对。

## 目标

1. **选型定案**：L0（页转场）用 ffmpeg `xfade`，不启动服务；L1（元素入场）用 Remotion。理由与排除项写进 design.md ADR-1，含对「纯 ffmpeg 能不能做 L1」的逐条能力核对。
2. **Remotion 合成器**：`renderer/` 独立工程，消费 `CompiledTimeline`，逐页产出 H.264 片段；时间数学只来自 IR，不解释动效名。
3. **Go backend**：`render.RemotionBackend` 实现既有 `Backend` 接口——staging（生成去残影底板）→ 写 plan → 调 Node → 回收产物；接口零改动，`cmd/renderer -backend remotion` 即可切换。
4. **镜像**：`deploy/renderer/Dockerfile`（base/app 两段），固定 Chrome 版本、OFL 字体、非 root、无外网、只读 rootfs。
5. **自检**：镜像内 `--selftest` 渲染固定 2 页样例并断言输出可解析；CI 与部署流水线跑它，而不是靠「镜像构建成功」当验收。
6. **样片**：用真实 IR 产出可播放样片，人工核对入场动画无残影、时间与时长对齐。

## 非目标

- 不做片段拼接、混音、烧字幕（属 M3/L5）。本变更只保证「片段本身可被 `-c copy` 拼接」。
- 不做 M6-2 的 DOM 后端。两端同帧 SSIM 是后续变更的验收，本变更只保证 MP4 端的插值语义与 IR 一致。
- 不做矢量重绘（L3）、不做 Lottie 精修。
- 不改 `internal/app/export.go`：M2 的 M3 接线仍按原计划单独变更。

## 与既有变更的关系

- 依赖 `animation-render-mvp` 的 `Backend` SPI 与 `CompiledTimeline`；本变更**不新增 IR 语义**，只加一个可选字段 `page.plateImage`（派生物，见 ADR-6）。
- 与 `html-share-bundle-static` 共用「底图降级」口径：缺图沿用上一页、首屏占位。渲染端不重复实现一套。
- 为 M3 提供前置：片段编码参数（H.264/yuv420p/固定 GOP/精确时长）在本变更固定下来，M3 的拼接才可能 `-c copy`。

## 平台决策

**镜像按 base/app 两层拆。** Chrome 是 CVE 的主来源、也是升级最频繁的部件；把它放在 base 层，升 Chrome 不必重装 node_modules，也不必重跑前端构建。代价是多一层镜像继承关系——比起每次安全更新都全量重建，这个代价可以接受。

**Chrome 版本进 planHash。** 同一份 IR 在不同 Chrome 上像素会差（字体栅格化、抗锯齿、合成器实现都会变）。缓存键不含 Chrome 版本，就会在升级后拿到旧像素，而且没人会发现。
