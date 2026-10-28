# html-share-bundle-static

M6-1：复用 web_project 打包能力，产出可双击打开的静态讲解 HTML 成品包

## 两种 zip 的区别（别混用）

| | `web_project` | `html_bundle` |
|---|---|---|
| 定位 | **数据包**：给下游程序消费 | **成品包**：给人看 |
| 内容 | `timeline.json` + `subtitles.srt/vtt` + `audio/*` | 上述之外再加 `index.html` + `manifest.json/.js` + `slides/*.jpg` + `app.js/app.css` + `audio/mix.wav` |
| 打开方式 | 打不开，需要自己写播放器 | 解压双击 `index.html` 即播放；也可放任意静态服务器 |
| 依赖 | 无（纯数据） | 无（零外链、断网可播、`file://` 可播） |
| 产物类型 | `web_project` | `html_bundle` |

`web_project` 保持不变是刻意的：已有下游按"zip 内含 timeline.json"解析，原地改内容属于隐性破坏。
新增类型可以独立灰度与回滚；`web_project` 的废弃另立决策。

## 怎么触发

- API：`POST /api/projects/{projectId}/exports`，body 为
  `{"format":"html_bundle","timelineKey":"...","pagePngKeys":[...]}`，需 `Idempotency-Key`
  （或 body 里的 `idempotencyKey`）。
- 前端：项目页/编辑器「导出」→ 选「HTML 成品包（双击可播）」。

## 包结构

```
index.html          成品首页（CSP、经典 <script> 引入，无内联脚本）
manifest.json       机读清单（slides/audio/subtitles/video/artifact 时间）
manifest.js         同一份清单的经典脚本形式（file:// 下可加载）
subtitles.vtt/.srt  与 MP4 烧录同源的 cues
subtitles.js        cues 的经典脚本形式（含字符级时间戳，用于逐字高亮）
slides/001.jpg...   页面底图（长边 ≤1600，JPEG q82）
audio/001.wav...    原始音频片段（按时间轴位置落位）
audio/mix.wav       按时间轴拼好的整段音轨（播放器优先使用，单一 currentTime 即全局时间）
app.js / app.css    播放器（IIFE + 样式，由 web/vite.share.config.ts 构建后入库）
timeline.json       时间轴原文（与 web_project 同名条目，便于下游平滑迁移）
```

## 隐私模式：本地生成（tasks 5.1–5.3）

云端打包走对象存储；隐私模式没有任何对象存储可读，走同一条打包逻辑的另一套读取实现：

```go
// 素材根目录下的相对路径，与对象存储 key 在打包器眼里同形
reader, _ := share.NewDirReader(root)              // os.DirFS，不解析 URL、不发请求
manifest, err := share.BuildLocalToDir(ctx, share.LocalBundle{
    Root:  root,
    Input: share.BundleInput{Timeline: timeline, PagePNGKeys: []string{"pages/001.png"}, VTTKey: "subs/cues.vtt", ...},
}, outDir)                                          // 直接产出可双击的目录
```

桌面端通过 sidecar 的 NDJSON 方法调用，不需要开本地 HTTP 端口：

```json
{"requestId":"e1","method":"export_bundle","payload":{
  "root":"/Users/me/proj","outDir":"/Users/me/proj/dist/v1",
  "timelineKey":"timeline.json","vttKey":"subs/cues.vtt","srtKey":"subs/cues.srt",
  "pagePngKeys":["pages/001.png","pages/002.png"],"title":"季度复盘","width":1920,"height":1080,"fps":30}}
```

约束与取舍：

- **root / outDir 必须是绝对路径**。sidecar 由 Tauri 拉起，工作目录不受调用方控制，相对路径会让"导出到哪"不可预测。
- **outDir 必须为空**（`ErrOutDirNotEmpty`）。包内按页序命名 `slides/001.jpg`，若上轮 5 页、本轮 3 页，覆盖写会留下看不见的 `slides/004.jpg`——体积偷偷涨、巡检还会当成内容泄漏。失败时本次写入的文件会被清理，不留半成品。
- **资源 key 一律相对 root**，且拒绝 URL / 绝对路径 / `..`（`checkResourceKey`）。Windows 的反斜杠会先归一成 `/` 再校验，避免 `os.DirFS` 把它当分隔符而绕过白名单。
- **结构一致性靠实现保证，不靠约定**：`Build`（zip）与 `BuildDir`（目录）都调用同一个 `buildInto` + `Manifest.Validate`，`TestLocalBundleMatchesZipStructure` 断言两者条目集合与清单逐字段相等。

验证器同样支持两种形态：`share.VerifyDir(dir)` 与 `share.VerifyBundle(zip)` 共用 `verifyEntries`。

## 体积基线（tasks 6.7）

`cmd/share-baseline` 生成可复现样本，`-pages-dir` 可换成真实导出的页图：

```bash
go run ./cmd/share-baseline -pages 30 -page-secs 10          # 5 分钟，合成页图
go run ./cmd/share-baseline -pages 30 -page-secs 10 -pages-dir ./real-pages -zip /tmp/b.zip
go run ./cmd/share-verify /tmp/b.zip                          # 结构 + 外链扫描
```

5 分钟（30 页 × 10s，1920×1080，24kHz 单声道 PCM16）实测：

| 分项 | 文件数 | 解压后体积 | 占比 |
| --- | ---: | ---: | ---: |
| 逐段音频 `audio/NNN.wav` | 30 | 13.46 MB | 47.1% |
| 混音轨 `audio/mix.wav` | 1 | 13.73 MB | 48.1% |
| 底图 `slides/*.jpg` | 30 | 1.32 MB | 4.6% |
| 播放器 `app.js/app.css` | 2 | 0.01 MB | 0.0% |
| 字幕 `subtitles.*` | 3 | 0.01 MB | 0.0% |
| 其他（index/manifest/时间轴） | 4 | 0.02 MB | 0.1% |
| **合计** | 70 | **28.55 MB** | 100% |

zip 2.4 MB（PCM 正弦波 deflate 压缩率极高，真实语音约 10–20%，zip 会接近 20MB+）。

结论（直接作为 M6-4 的输入）：

1. **音频占 95.2%，且同一份声音存了两遍**——逐段 `audio/NNN.wav` 与 `audio/mix.wav` 内容等价。这是第一大优化项：只保留混音轨可立减约 47%，代价是失去"混音拼接失败则逐段播放"的降级路径，需要改成"拼接在打包期保证成功，失败则整包失败"或"按需切片"。
2. **音频编码格式比底图重要一个数量级**。24kHz PCM16 转 AAC 64kbps，5 分钟从 14.4MB 降到约 2.4MB（6×）。这是容器决策（纯 ffmpeg L0 vs Remotion）的一部分，不只是体积问题。
3. **底图在合成样本里只占 4.6%，但真实 PPT 截图 JPEG 通常 150–400KB/页**，30 页将是 4.5–12MB，占比升到 25–45%。WebP 仍有价值（同质量约省 25–35%），但排在音频之后。
4. **字体子集当前为 0 字节**（`manifest.fonts` 为空，播放器只用系统字体回退）。若 M6-4 决定内嵌，中文单字重子集约 1–3MB，会直接吃掉底图省下来的空间。
5. 建议门禁阈值：5 分钟成品包 zip ≤ 25MB。当前合成样本 2.4MB；真实样本待 `-pages-dir` 复跑后回填。

## 落地范围与偏差（tasks 7.4）

原设计方案《动画视频扩展-设计方案-V1_0.md》已不在仓库中（`docs/` 在 1fd113d 被移出仓库），
因此落地范围与偏差记录在本文件与 `design.md §Implementation Notes`，随代码一起版本化。

已完成：产物类型与打包器、IIFE 播放器、导出接线（Connect 之外的 REST 入口）、
隐私模式本地生成、结构/外链门禁工具、CI 漂移检查与外链扫描。

与方案不同之处：

| 方案 | 实际 | 原因 |
| --- | --- | --- |
| 解析 VTT 得到 cues | 直接取 `Timeline.Subtitles`，VTT 由同一份 cues 渲染 | 解析 VTT 会丢掉 `chars`，逐字高亮失效；两份解析必然漂移 |
| 导出请求通过 Connect 选 `html_bundle` | 新增 REST `POST /api/projects/{id}/exports` | 扩展 proto enum 需重新生成 pb.go，本机无 buf/protoc |
| 底图 WebP、音频 AAC、字体子集 | 本阶段均未做：JPEG q82 / 原样 WAV / 不内嵌字体 | 见上节体积基线：先解决音频编码与双份存储，收益最大 |
| 5 分钟样片人工盲评 | 无头 Chrome 核对了 `file://` 加载、CSP、字幕断行、首屏渲染 | 无 ffmpeg/Remotion 镜像；音频解码在无头环境不可靠，点击播放需真机验收 |

