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
