# Tasks

> 交付纪律：本变更对应设计方案 M6-1，不依赖 M1/M2，可与 `animation-spec-core` 并行。合并前必须通过 `go build ./...`、`go vet ./...`、`go test ./internal/share/... ./internal/app/... ./internal/artifact/...`，前端 `npx tsc --noEmit` 与 `npm run build:share`，且 `git diff --exit-code internal/share/assets/` 为空。

## 1. 产物类型与骨架（PR1）

- [x] 1.1 `internal/artifact` 新增 `FormatHTMLBundle = "html_bundle"`，补齐下载文件名后缀（`.zip`）、MIME（`application/zip`）与产物库展示文案
- [x] 1.2 迁移/兼容检查：既有 `web_project` 记录的解析、下载、展示 MUST 不受影响（加一条回归用例）
- [x] 1.3 `internal/share/bundle.go` 定义 `BundleInput{Timeline, PagePNGKeys, VTT, SRT, AudioKeys, Width, Height, FPS, Title}` 与 `BundleBuilder` 骨架，先只产出 `timeline.json` + `audio/*` + `subtitles.*`（等价于旧包），保证可增量落地
- [x] 1.4 `ResourceReader` 接口 + 对象存储实现，流式写入 zip（`zw.Create` + `io.Copy`），不驻留内存

## 2. 打包器核心（PR1）

- [x] 2.1 `manifest.go`：由 `bundle.Timeline` 派生 `manifest.json`（`slides`/`audio`/`subtitles`/`video`/`artifact`），时间全部取自 `StartUS/EndUS`，不引入第二套时间
- [x] 2.2 `images.go`：`image/png` 解码 + 长边 ≤1600 缩放 + `image/jpeg` q82 编码；解码失败回退原始字节并记日志（不阻断）
- [x] 2.3 底图缺失降级：按 `PagePNGKeys` 位置对齐，缺键/读失败沿用上一页，首屏缺失用占位图（与 `export.go` 现有语义一致）
- [x] 2.4 生成 `index.html`（`text/template`，注入 CSP meta、`<script src="app.js">`、`<link rel="stylesheet" href="app.css">`、`<noscript>` 兜底）
- [x] 2.5 生成 `manifest.js` 与 `subtitles.js`（经典脚本，`window.__PPT_MANIFEST__` / `window.__PPT_SUBTITLES__`），VTT→cues 的解析放在 Go 侧并单测
- [x] 2.6 打包路径校验：拒绝绝对路径、`http(s)://`、对象存储 key 形态的资源名

## 3. 分享播放器（PR2）

- [x] 3.1 `web/src/sharePlayer/main.ts`：读取 `window.__PPT_MANIFEST__`/`__PPT_SUBTITLES__`，直接 `import` `../playerClock` 的 `slideAt`/`spokenCharAt`/`splitLines`/`progress`
- [x] 3.2 以音频 `currentTime` 为权威时钟驱动切页与逐字高亮；仅在 `seek`/卡顿时校正，不做每帧赋值
- [x] 3.3 UI：播放/暂停、进度条拖动、上一页/下一页、字幕开关、封面与「点击播放」手势兜底
- [x] 3.4 键盘：`Space`、`←`、`→`；`aria-label` 与 `role` 基础无障碍标记；字幕容器 `aria-live="off"`
- [x] 3.5 `web/vite.share.config.ts`：`lib` 模式、`formats: ["iife"]`、产物输出到 `internal/share/assets/`；`package.json` 增加 `build:share`
- [x] 3.6 `internal/share/assets/assets.go`：`//go:embed app.js app.css`，构建产物入库

## 4. 导出接线（PR2）

- [x] 4.1 `internal/app/export.go` 新增 `case artifact.FormatHTMLBundle:` 分支与 `packHTMLBundle`，与 `packWebProject` 并列，不改动既有分支
- [x] 4.2 组装上下文：`snapshot.PagePNGKeys`、`bundle.Timeline`、VTT/SRT key、音频 key 注入 `BundleBuilder`
- [x] 4.3 错误分类：打包失败按可重试/永久分类，与既有导出错误语义对齐；不影响 `classifyEncodeError`
- [x] 4.4 `internal/api` 路由与请求参数：允许在导出请求中选择 `html_bundle`，默认值不变（默认仍为 mp4）

## 5. 隐私模式本地生成（PR3）

- [ ] 5.1 新增本地 `ResourceReader` 实现（`os.DirFS` + 本地音频/字幕路径）
- [ ] 5.2 本地生成路径不触碰对象存储、不发网络请求；产物写用户指定目录
- [ ] 5.3 断言：本地包结构与云端包结构一致（同一份结构校验函数复用）

## 6. 测试与验收

- [x] 6.1 Go 单测：解压产物 → 文件齐备、清单时间自洽（误差 ≤100ms）、`src/href/url()` 外链计数 = 0
- [x] 6.2 Go 单测：VTT→cues 解析结果与既有 VTT 行序一致；断行参数与 `subtitle.go` 注释所述规则一致
- [x] 6.3 Go 单测：缺图降级（中间页缺、全缺）、转码失败回退、资源名越界拒绝
- [x] 6.4 golden 包用例：2 页小样本，比对文件列表与清单结构（不比字节）
- [x] 6.5 前端：`npx tsc --noEmit`、`npm run build:share`、既有 `contrast`/lint 脚本通过
- [x] 6.6 人工验收：双击 `index.html` 播放、断网播放、拖进度对齐、切页、关字幕、移动端点击播放
- [ ] 6.7 记录体积基线（5 分钟样本：底图 / 音频 / 播放器 / 总计），写入变更说明，作为 M6-4 体积门禁的输入

## 7. CI 与文档

- [x] 7.1 CI 增加 `npm run build:share && git diff --exit-code internal/share/assets/`（防止生成物与源码漂移）
- [ ] 7.2 CI 增加产物外链扫描用例（复用 6.1 的检查函数）
- [x] 7.3 更新 `docs/` 中关于 `web_project` 的说明，明确 `web_project`（数据包）与 `html_bundle`（成品包）的区别与各自用途
- [ ] 7.4 在方案文档中回写 M6-1 实际落地范围与偏差（含未做的 WebP/AAC/字体子集）

---

## 落地状态（2026-10-28）

**已完成：1、2、3、4 全部。** PR1（打包器）+ PR2（播放器 + 接线）已经合并为一次提交，可产出一个
「解压双击即播」的真实成品包。

**完成但实现方式与任务描述不同（偏差已在 design.md §Implementation Notes 记录）：**

- 2.5 的「VTT→cues 解析」改为**直接用 `Timeline.Subtitles`**（含字符级时间戳，VTT 由同一份 cues 渲染），
  不再解析 VTT 文本。理由：解析 VTT 会丢掉 `chars`，逐字高亮就没了；且两份解析必然漂移。
  6.2 因此改为断言 `subtitles.js` 携带与时间轴一致的 cue，`subtitles.vtt` 原样入包。
- 4.4 的「导出请求选择 html_bundle」无法走 Connect：新增格式要在 proto enum 加值并重新生成 pb.go，
  本机与 CI 之外的开发环境都没有 buf/protoc。改为新增 REST 端点
  `POST /api/projects/{projectId}/exports`（与 `/api/context-rules`、动画端点同一先例），
  产物 snapshot 与导出处理器与 Connect 入口完全一致。
- 6.6 的「人工验收」用无头 Chrome 完成了一部分：`file://` 加载、CSP 放行、脚本执行、字幕断行、
  首屏渲染均已截图核对；**点击播放/拖动/翻页仍需人在真机上点一次**（无头环境的音频解码不可靠）。

**未完成（下一轮）：** 5.1–5.3（隐私模式本地生成）、6.7（5 分钟体积基线）、7.2、7.4。

**已知与本变更无关的红灯：** `web/i18n-check.mjs` 有 12 条历史零引用词条
（`user.*` / `login.*` 等，早于本变更），本变更新增的 4 个词条均被引用、中英键集合对等。
按纪律不为"顺手绿一下"去删别人的文案，留待词条归属人处理。
