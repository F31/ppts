# html-share-bundle Specification

## Purpose
定义「静态讲解 HTML 成品包」的包结构、播放清单、播放器行为与自包含约束。目标是让用户拿到 zip 或托管目录后，双击/打开链接即可播放一段带音频、字幕与逐字高亮的讲解，且不依赖任何网络资源与后端服务。

## Requirements

### Requirement: 产物为自包含可播放包

导出的 HTML 成品包 MUST 在根目录包含 `index.html`，解压后 MUST 能在 `file://` 与静态文件服务器两种打开方式下播放，MUST NOT 依赖任何后端接口。

#### Scenario: 解压双击打开

- **WHEN** 用户下载 zip、解压并双击 `index.html`
- **THEN** 页面加载后可直接播放，出现首屏底图与播放控件，控制台无错误

#### Scenario: 静态托管打开

- **WHEN** 同一份包被放到任意静态文件服务器（含 `internal/api` 的 `http.FileServer`）根路径
- **THEN** 播放表现与本地打开一致

### Requirement: 包结构与清单契约

包内 MUST 包含 `manifest.json`，其 MUST 声明 `schemaVersion`、`video{width,height,fps}`、`slides[{index,image,startUS,durationUS}]`、`audio` 片段列表、`subtitles.src`、`artifact.durationUS`。清单中的路径 MUST 为包内相对路径，MUST NOT 出现绝对路径或对象存储 key。

#### Scenario: 清单自洽

- **WHEN** 校验器读取 `manifest.json`
- **THEN** 每个 `slides[i].image` 与音频条目 MUST 在包内存在，且 `slides` 时间累加与 `artifact.durationUS` 误差 ≤ 100ms

#### Scenario: 拒绝绝对路径

- **WHEN** 打包时发现某资源路径以 `/`、`http`、`file:` 或对象存储 key 形式出现
- **THEN** 打包 MUST 失败并报出该资源，MUST NOT 生成带外链的包

### Requirement: 播放以音频为权威时钟

播放器 MUST 以音频 `currentTime` 派生当前页与当前高亮位置，MUST NOT 用 `requestAnimationFrame` 自行累加时间。页面切换与字幕行轮换 MUST 与音频位置一致，长时间播放（≥ 5 分钟）MUST NOT 出现累积漂移。

#### Scenario: 播放到中段

- **WHEN** 音频播放至第 3 页 60% 处
- **THEN** 展示的页 MUST 为第 3 页，字幕行与高亮字符 MUST 与音频位置匹配，误差 ≤ 100ms

#### Scenario: 拖动进度条

- **WHEN** 用户把进度拖到任意位置
- **THEN** 页面、字幕与高亮 MUST 立即对齐该位置，MUST NOT 出现只在下次跳页才生效的情况

### Requirement: 字幕与 MP4 同源同规则

包内字幕 MUST 来自既有 `subtitles.vtt`（`internal/media/subtitle.go` 产出的同一份 cues），MUST NOT 在打包或播放阶段从讲稿重新生成字幕。为保证 `file://` 下可播放，打包器 MAY 把该 VTT 转写为包内 `subtitles.js`（经典脚本形式的同一份 cues），且 MUST 同时保留原始 `subtitles.vtt`。断行与高亮 MUST 复用 `web/src/playerClock.ts` 的 `slideAt` / `spokenCharAt` / `splitLines`，MUST NOT 实现第二套规则。

#### Scenario: 与烧录字幕一致

- **WHEN** 对同一讲稿比较 HTML 播放器显示的字幕行与 MP4 烧录的 ASS 行
- **THEN** 两者的断行结果与切行时刻 MUST 一致（允许字体度量差异导致的像素差异，不允许行内容与行序不同）

### Requirement: 播放控制完整

播放器 MUST 提供播放/暂停、进度拖动、上一页/下一页、字幕开关；键盘 `Space`、`←`、`→` MUST 可用。音频被浏览器自动播放策略拦截时 MUST 展示封面并要求一次点击后播放，MUST NOT 静默失败。

#### Scenario: 移动端自动播放被拦截

- **WHEN** 页面在需要手势的环境下加载
- **THEN** 展示封面与「点击播放」，点击后从 0 开始正常播放

#### Scenario: 关闭字幕

- **WHEN** 用户关闭字幕开关
- **THEN** 字幕行与逐字高亮 MUST 隐藏，页面切换与音频 MUST 不受影响

### Requirement: 无外部引用

包内 MUST NOT 存在任何外部网络引用：HTML/CSS/JS 中不得出现 `http(s)://`、协议相对地址、外链字体或外链图片；字体 MUST 使用系统字体族回退（本阶段不内嵌字体）。

#### Scenario: 断网加载

- **WHEN** 在断网环境下打开 `index.html`
- **THEN** 页面 MUST 完整播放，MUST NOT 出现白屏、字体方框或 404

#### Scenario: 静态扫描

- **WHEN** 对包内所有文件做 `src` / `href` / `url()` 扫描
- **THEN** 外部引用计数 MUST 为 0

### Requirement: 页面底图缺失可降级

打包时若某页底图缺失或读取失败，该页 MUST 沿用上一页画面；首屏缺失 MUST 使用与 MP4 路径一致的占位图。任一页缺图 MUST NOT 导致导出失败。

#### Scenario: 中间页缺图

- **WHEN** 第 4 页对象不存在，其余页正常
- **THEN** 第 4 页展示第 3 页画面，其余页正常，产物完整可播放

#### Scenario: 全部缺图

- **WHEN** 所有页底图均缺失
- **THEN** 包仍生成成功，每页使用占位图，音频与字幕正常播放

### Requirement: 底图转码受约束

页面底图 MUST 以长边 ≤ 1600px 输出，格式 MUST 为 JPEG 且质量 ≤ 82；转码失败 MUST 回退使用原始 PNG 而不阻断导出。转码 MUST NOT 改变页面宽高比。

#### Scenario: 1080p 底图

- **WHEN** 输入 1920×1080 PNG
- **THEN** 输出 1600×900 JPEG，宽高比误差 ≤ 0.5%，单页体积显著小于原 PNG

#### Scenario: 转码失败

- **WHEN** 某页 PNG 解码失败
- **THEN** 该页回退原始字节并继续打包，日志记录该页与原因

### Requirement: 既有产物类型不受影响

新增 `html_bundle` MUST NOT 改变 `web_project` 的内容与字段；两种产物 MUST 可由产物类型字段区分。

#### Scenario: 旧包仍可下载

- **WHEN** 用户下载一个历史 `web_project` 产物
- **THEN** 包内容与字段与本次变更前完全一致

### Requirement: 隐私模式可本地生成

在隐私模式（本地 sidecar）下，HTML 成品 MUST 可在本机生成，MUST NOT 要求对象存储或在线托管参与；本地生成的包内容 MUST 与云端产物结构一致。

#### Scenario: 本地导出

- **WHEN** 隐私模式下触发 HTML 成品导出
- **THEN** 产物落在本地指定目录，可直接打开播放，且无任何出网请求
