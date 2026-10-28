# Design

## Context

已核实的现状（`E:/projects/ppts`，main 分支）：

| 事实 | 位置 |
|---|---|
| `packWebProject` 打 zip：`timeline.json` + `subtitles.srt` + `subtitles.vtt` + `audio/*`（音频流式写入，不驻留内存） | `internal/app/export.go:315` |
| 产物类型 `artifact.FormatWebProject = "web_project"`，已通存储/下载/产物库 | `internal/app/export.go:151` |
| 页面底图以 `snapshot.PagePNGKeys` 形式已就绪，缺键/读失败已有「沿用上一页 + 首屏占位」降级（`blankPagePNG`，纯标准库） | `internal/app/export.go:443-475` |
| 播放器逻辑 `Player.tsx`（538 行）+ `playerClock.ts`（91 行，`slideAt`/`spokenCharAt`/`splitLines`/`progress` 四个纯函数） | `web/src/` |
| 烧录字幕与网页播放器共用同一套断行参数 | `internal/media/subtitle.go` 注释 |
| 静态托管通道 `http.FileServer` 支持 `os.DirFS` 与 `embed.FS` | `internal/api/server.go` |
| 前端依赖仅 vite/react/react-dom/typescript | `web/package.json` |

约束：Go 侧构建不能依赖 Node；包必须能在 `file://` 下播放；运行时零新增依赖。

## Goals / Non-Goals

**Goals:**

- 让 `web_project` 从「数据包」变成「可播放成品」，且不改动旧产物语义。
- 播放体验（分页、字幕、逐字高亮、进度）与现有 React 播放器一致，逻辑单源。
- 断网可播、`file://` 可播、零外链。
- 隐私模式可本地生成。

**Non-Goals:**

- 动画层（M6-2）、成品级 token/在线分享（M6-3）、字体子集、WebP、AAC、SSIM/无头浏览器门禁（M6-4）。
- 编辑能力、章节跳转、倍速、复制开关。
- 改动 `ShareLink` / `publication`。

## Decisions

### D1 新增 `html_bundle` 产物类型，不原地改 `web_project`

`web_project` 已对外可用（协议、存储、下载、产物库），改变其内容属于隐性破坏：下游若按「zip 内含 timeline.json」解析，会因新增文件或结构变化而出错。

因此：`FormatHTMLBundle = "html_bundle"` 作为新类型；`packWebProject` 保留原样，新增 `packHTMLBundle` 并列分支。代价是多一处分支，收益是可独立灰度与回滚。`web_project` 的废弃另立决策，不在本变更内。

### D2 播放器用经典脚本（IIFE），不用 ES module / fetch

这是本设计最关键的一条，直接决定「双击能不能播」：

- `file://` 下 `fetch()` 与 `<script type="module">` 受 CORS 限制，Chrome/Edge 会直接失败。
- `<audio src>`、`<img src>`、`<link rel=stylesheet>` 属于同源资源加载，`file://` 下可用。
- `<track src="*.vtt">` 在 `file://` 下同样受 CORS 限制，不可依赖。

结论：

1. 播放器编译为 **IIFE 单文件** `app.js` + `app.css`，用经典 `<script src>` 引入。
2. 清单与字幕以**经典脚本**形式内联为 `manifest.js`、`subtitles.js`（`window.__PPT_MANIFEST__` / `window.__PPT_SUBTITLES__`），避免任何 `fetch`。
3. 仍然保留 `manifest.json` 与 `subtitles.vtt` 作为机读/交换格式（外部工具、后续 M6-3 托管页可用），但播放器不依赖它们。

备选：全部 base64 内联进 `index.html`——被否决，因为 M6-1 目标之一是后续目录形态的懒加载；内联留给「单文件形态」这个 M6-4 选项。

### D3 逻辑单源：复用 `playerClock.ts`，不复制

`web/src/sharePlayer/main.ts` 直接 `import { slideAt, spokenCharAt, splitLines, progress } from "../playerClock"`。构建产物只有一个入口，UI 用原生 DOM 重写（约 200–300 行），但**时间数学零重复**。

构建与产物管理：

- `web/vite.share.config.ts`：`build.lib = { entry: "src/sharePlayer/main.ts", formats: ["iife"], name: "PptsSharePlayer" }`，输出到 `internal/share/assets/app.js` 与 `app.css`。
- **构建产物入库**（`internal/share/assets/` 提交到 git，用 `go:embed` 打包），保证 `go build` 不需要 Node。
- CI 增加一致性检查：`npm run build:share` 后 `git diff --exit-code internal/share/assets/`，防止「改了 TS 忘了重新生成」。

这与仓库既有的「生成物入库 + CI 校验未重新生成即失败」纪律一致（catalog 生成同理）。

### D4 清单结构：以 Timeline 为唯一时间来源

`manifest.json` 由 `bundle.Timeline` 直接派生，不引入第二套时间：

```jsonc
{
  "schemaVersion": 1,
  "artifact": { "durationUS": 312400000, "snapshotHash": "sha256:..." },
  "video": { "width": 1920, "height": 1080, "fps": 30 },
  "slides": [{ "index": 0, "image": "slides/001.jpg", "startUS": 0, "durationUS": 8400000 }],
  "audio":  [{ "src": "audio/seg-001.wav", "startUS": 0, "durationUS": 2100000 }],
  "subtitles": { "src": "subtitles.vtt", "inline": "subtitles.js" },
  "fonts": []
}
```

`startUS`/`durationUS` 全部来自 `Timeline.Slides[*].StartUS/EndUS` 与 `Segments[*]`，`slides` 与 `audio` 的时间线 MUST 自洽（spec 已要求误差 ≤100ms）。页面与音频的相对关系沿用现有 `WriteTimelineWAV` 的落位规则，因此 HTML 与 MP4 的时序天然一致。

> 落地修正：实际字段名是 camelCase（`startUs`/`endUs`/`durationUs`），与 `media.Timeline` 的 JSON 一致，
> 便于播放器把清单直接交给 `slideAt`。见文末 Implementation Notes 1。

### D5 底图转码用标准库 JPEG，失败回退原始字节

- 解码：`image/png`；缩放：整数倍盒式下采样到长边 ≤1600（1080p 时倍率 1，仅当 4K 源才缩），再按需单次双线性到目标尺寸——与 `blankPagePNG` 的「只用标准库、直接操作 `img.Pix`」先例一致。
- 编码：`image/jpeg` q82。
- 失败（解码错、尺寸异常）：原样放入原 PNG，记 `animation_degrade_total{stage="thumbnail"}` 同类指标（复用既有指标命名风格）。
- 为什么不是 WebP：标准库无编码器，引入 `golang.org/x/image` 违反「无明确不可替代性不新增依赖」。WebP 留到 M6-4 与体积门禁一起评估。

### D6 打包器按「资源读取接口」注入，天然支持隐私模式

```go
type ResourceReader interface {
    OpenPagePNG(ctx context.Context, key string) (io.ReadCloser, error)
    OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
}
```

- 云端实现：对象存储（现有 `h.readTenantObject` 的流式版本）。
- 本地实现：`os.DirFS`。
- `BundleBuilder` 只依赖接口 + `*TimelineAsset`，不感知租户与存储细节。

流式写入 zip（`zw.Create` + `io.Copy`）而非先读入内存，沿用 `packWebProject` 已验证的做法，避免 100 页 deck 打爆内存。

### D7 CSP 与转义

`index.html` 由 `text/template` 生成，注入 `Content-Security-Policy`：

```
default-src 'none'; img-src 'self' data:; media-src 'self';
style-src 'self' 'unsafe-inline'; script-src 'self'; base-uri 'none'; form-action 'none'
```

- 无内联脚本（清单与字幕是外部 `.js`），因此不需要 `'sha256-...'`。
- PPT 文本（标题、讲稿）作为**不可信输入**，写入 HTML 时一律 `html/template` 转义；`text/template` 只用于拼接已知常量结构，二者不混用。

### D8 降级与错误边界

| 情况 | 行为 |
|---|---|
| 底图缺失/读失败 | 沿用上一页；首屏缺失用 `blankPagePNG` 等价占位 |
| VTT 缺失 | 打包失败（字幕是成品的一部分） |
| 音频片段缺失 | 打包失败并指出 key（缺音频的「成品」没有意义） |
| 播放器资源 `go:embed` 缺失 | 构建期失败（`embed` 保证），不留运行时死角 |
| 浏览器不支持 `<audio>` | 展示静态页 + 字幕文本（`<noscript>` 与能力检测） |

### D9 验收方式（本阶段不引无头浏览器）

- Go 单测：解压产物 → 断言文件齐备、清单自洽、`src/href/url()` 扫描外链数 = 0、HTML 不含未转义文本。
- 一份 golden 包（小样本 2 页）入库，比对结构而非字节（zip 时间戳不稳定）。
- 人工验收清单：双击打开、断网打开、拖进度、切页、关字幕、移动端点击播放。
- 无头浏览器与 SSIM 属 M6-4，本变更不承诺。

## Risks / Trade-offs

| 风险 | 缓解 |
|---|---|
| 生成物入库导致仓库膨胀（app.js 约 30KB） | 只提交压缩后产物，且仅两个文件；CI 校验避免反复无意义 diff |
| 播放器 UI 与 React `Player.tsx` 长期分叉 | 时间数学单源；UI 差异登记为已知项，M6-3 若做站内播放再评估统一 |
| 音频仍为 WAV，5 分钟约 5MB | 本阶段只记录体积指标；AAC 转码与体积门禁在 M6-4，避免过早引入 ffmpeg 依赖到打包器 |
| `file://` 下各浏览器行为差异 | 以 Chrome/Edge 100+、Safari 15+ 为支持矩阵；不承诺 IE 与老旧 WebView |
| 清单与 Timeline 语义漂移 | 清单字段全部由 Timeline 派生，单测断言时间自洽；M6-2 引入动画后复用同一清单 |

## Implementation Notes（落地回写，2026-10-28）

落地过程中有五处与上面的草案不同，均已生效在代码里：

1. **清单字段用 camelCase，不是 D4 草案的 `startUS`/`durationUS`。** `media.Timeline` 的 JSON 就是
   `startUs`/`endUs`/`durationUs`，播放器要把 `manifest.slides` 直接喂给 `slideAt`（它读 `startUs`），
   两套命名只会制造转换层。清单因此与时间轴同构，`slides[i]` 就是带 `image` 的 `SlideCue`。
2. **字幕 cues 直接取 `Timeline.Subtitles`，不解析 `subtitles.vtt`。** VTT 是这些 cues 的渲染结果，
   反过来解析会丢掉 `chars`（逐字高亮靠它）。`subtitles.vtt` 仍原样入包作为交换格式。
   这是 2.5 的实现偏差，理由记录在 tasks.md。
3. **新增 `audioMix`：一条按时间轴落位的 WAV。** 播放器优先用它，`currentTime` 直接等于全局时间；
   `manifest.audio` 仍列原始片段（M6-4 做 AAC 与体积门禁时按段处理）。混音拼接失败（素材非 PCM16、
   时长与时间轴不符）**不阻断打包**，播放器回退到逐段播放。
4. **`html_bundle` 的导出入口是 REST，不是 Connect。** 新增格式需要给 proto enum 加值并重新生成
   pb.go，当前构建环境没有 buf/protoc。`POST /api/projects/{projectId}/exports` 与既有
   `ExportService.CreateExport` 产出**同一结构**的 `ExportSnapshot`，由同一个 `ExportHandler` 消费。
   路由已在 `web/route-inventory.json` 登记为 `keep-both`，收敛条件写在该条目里。
5. **CSP 显式允许 `file:` scheme。** 双击打开时页面源是 `file://`，只写 `'self'` 在 Chrome/Safari 下
   会拦掉 `app.js`/`app.css`，页面直接白屏——而"双击能播"是这个产物唯一的存在理由。
   `file:` 在 http 托管下不会被用到（浏览器禁止 http 页面加载 file 资源），不放宽任何远程来源。

验收补充：用系统 Chrome 的 `--headless=new --dump-dom` 与 `--screenshot` 在 `file://` 下核对了
脚本执行、CSP 放行、字幕断行与首屏渲染（截图见提交说明）；音频推进与指针交互仍需真机点一次。

