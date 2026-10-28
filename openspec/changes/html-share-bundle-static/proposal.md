# Proposal

## Why

`artifact.FormatWebProject` 已经在打 zip（`internal/app/export.go:315` `packWebProject`），包里是 `timeline.json` + `subtitles.srt` + `subtitles.vtt` + `audio/*`——**有数据、没画面**：没有 `index.html`、没有页面底图，用户拿到压缩包双击是打不开的，必须自己写播放器。

而「点开就能看」在分享场景是刚需：微信/企微内打开、官网嵌入、邮件附件，mp4 都不是合适载体。这条路径不依赖任何动画能力，是设计方案里**最早可交付、最快能演示**的成果（§20.9：M6-1 无依赖，可与 M1 并行）。

## What Changes

- 新增 `artifact.FormatHTMLBundle`（`"html_bundle"`）产物类型；既有 `web_project` 保持不变，避免破坏现有下载方。
- `packWebProject` 的产物升级为**可播放成品包**：新增 `index.html`、`manifest.json`、`slides/*.jpg`（页面底图）、内联播放器脚本与样式。
- 新增一个**框架无关**的分享播放器入口（`web/src/sharePlayer/`），由既有 Vite 构建产出单文件 JS/CSS，经 `go:embed` 打进 Go 二进制；其时间计算直接复用 `web/src/playerClock.ts`，不复制一份逻辑。
- 页面底图取自 `ExportSnapshot.PagePNGKeys`（静态导出路径已有），用标准库 `image/jpeg` 按长边 ≤1600、q82 转码；缺图的页沿用上一页，首屏缺失用占位图，不阻断导出。
- 新增 `internal/share` 打包器：`BundleBuilder` 负责组装 `index.html` / `manifest.json` / 资源目录，`ExportHandler` 只负责把现有上下文（timeline、音频、VTT、底图）交给它。
- 隐私模式下可在本地生成（不依赖对象存储与在线托管）。
- **不包含**：动画层（M6-2）、成品级 token 与在线分享（M6-3）、字体子集化与一致性门禁（M6-4）。

## Capabilities

### New Capabilities

- `html-share-bundle`: 静态讲解成品的自包含打包与播放契约——包含包结构、清单字段、播放器行为（以音频为权威时钟、与 MP4 同规则的字幕）、离线自包含约束与降级行为。

### Modified Capabilities

（无。`web_project` 的既有行为与字段不变；`share_link`、`publication` 本变更不涉及，留给 M6-3。）

## Impact

**新增代码**

- `internal/share/`：`bundle.go`（组装 zip）、`manifest.go`（清单结构）、`images.go`（std-lib JPEG 转码）、`assets/`（`go:embed` 的播放器产物）
- `web/src/sharePlayer/`：`main.ts`、`index.html.tmpl`、`player.css`；复用 `web/src/playerClock.ts`
- `web/vite.share.config.ts`：独立构建配置，产物输出到 `internal/share/assets/`
- `internal/artifact/`：新增 `FormatHTMLBundle` 常量与下载/展示适配
- `internal/app/export.go`：新增 `packHTMLBundle` 分支（与 `packWebProject` 并列，不改动既有分支）

**依赖**

- 运行时零新增：zip 用 `archive/zip`，模板用 `text/template`，图像用 `image/png` + `image/jpeg`。
- 构建期：复用既有 `vite`（已在 `web/package.json`），不新增 devDependency。
- 明确不引入：JS 打包器之外的框架、字体子集工具（`pyftsubset` 属 M6-4 渲染容器）。

**受影响系统**

- 导出产物类型与下载适配（新增一种，不改变旧类型语义）
- 产物库展示（需要能识别 `html_bundle` 并给出「打开/下载」两种动作）

**风险**

- 底图转码会增加导出耗时（20 页 1080p PNG → JPEG 约 1–2s）→ 与编码并行，失败则回退原 PNG。
- 播放器渲染未使用 React，若与 `Player.tsx` 交互不一致会产生「两个播放器」认知负担 → 复用 `playerClock.ts` 纯函数，UI 只做骨架简化。
- 音频保持原格式会推高体积 → 本阶段只记录指标，AAC 转码与体积门禁放 M6-4。
