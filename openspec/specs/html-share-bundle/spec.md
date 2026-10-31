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

`slides[i]` MAY 另带 `shots[]`（页内镜头切片）与 `layers[]`（元素动画层）。两者都是可选字段：缺省时产物 MUST 与引入它们之前逐字节一致。

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

### Requirement: 成品包携带分层资源与动画数据

当动画开关开启时，包 MUST 为每个含 clip 的元素提供独立位图（只含该元素、已按其 `box` 裁切），并把该页的 `layers[*].clips` 原样写入清单。底板（挖空元素本体后的页面图）MUST 在同一份原始页图上生成，MUST NOT 在降采样或 JPEG 转码后的图上做取色。

`layers` 数据 MUST 与渲染计划里的 IR 字段一一对应，MUST NOT 在导出期做部分预编译（属性展开、clip 合并等）后写入——预编译会引入第二套插值实现。

#### Scenario: 有元素动画的页

- **WHEN** 某页存在 `layers[*].clips` 非空
- **THEN** 清单该页 `layers` 中每项都有 `image`（包内路径）、`box`（画布像素）、`clips`（IR 原样），
  且 `image` 指向的位图只含该元素像素
- **AND** 该页 `image` 指向挖空底板

#### Scenario: 无元素动画的页

- **WHEN** 某页 `layers` 为空
- **THEN** 该页 `image` 为完整页面底图，`layers` 为空数组或省略，播放器按 M6-1 的静态逻辑处理

### Requirement: DOM 动画以音频为权威时钟并由 WAAPI 回放

播放器 MUST 以 `audio.currentTime` 作为动画的唯一时间源，MUST NOT 用 `requestAnimationFrame` 自行累计时间。关键帧 MUST 在首次进入该页时编译一次，回放阶段 MUST 只通过 WAAPI 的 `play/pause/currentTime` 驱动，MUST NOT 在播放循环里逐帧写 `style`。

clip 的叠加语义 MUST 与渲染端一致：`opacity` 相乘、`translate*` 相加、`scale*` 相乘。同属性多条 clip 时间重叠时，MUST 在该区间内按统一求值函数采样合并，MUST NOT 依赖 WAAPI 默认的 `replace` 语义。

#### Scenario: 播放中 seek 到动画进行中的时刻

- **WHEN** 用户在某一页动画进行中拖动进度条
- **THEN** 该页所有 animation 先被批量 `pause()` 并设到目标 `currentTime`，再按原播放状态恢复，
  画面与同时刻 MP4 帧的位置/不透明度一致，且不出现闪烁

#### Scenario: 切页

- **WHEN** 播放进入下一页
- **THEN** 上一页全部 animation 被 `cancel()`，不残留在合成层

#### Scenario: 同一元素两条重叠 clip

- **WHEN** 某元素存在两条时间区间重叠、同属性的 clip
- **THEN** 编译期按 `evaluateLayer` 采样合并为一条 keyframe 序列，
  在重叠区间内该时刻的数值等于渲染端同一函数的结果

### Requirement: 两端同帧 SSIM 不低于 0.95

验收 MUST 在**动画进行中**取帧：每页取第一条 clip 的中点时刻，另加首帧与末帧。判定 MUST 用逐页最差帧，MUST NOT 用全片平均。

比较基准 MUST 先量化 H.264 编码损失：若同一张源图编解码后的 SSIM 天花板本身低于阈值，门禁 MUST 改为与「同一段静态画面的编码帧」比较，而不是与未编码源图比较。

#### Scenario: 动画中点帧

- **WHEN** 对某页 `startMs + durationMs/2` 时刻，MP4 解码帧与浏览器截图比较
- **THEN** SSIM ≥ 0.95

#### Scenario: 某一页不达标

- **WHEN** 任一页任一取样时刻 SSIM < 0.95
- **THEN** 变更 MUST NOT 通过验收，且报告必须指出该页该时刻，而不是只给一个全片均值

### Requirement: 动画关闭时零副作用

动画开关关闭时，包内 MUST NOT 含 `layers` 或非 `none` 的 `transition`，产物 MUST 与 M6-1 的包在画面内容上逐像素一致（`createdAt`/`snapshotHash` 这类天然变化的字段除外）。

#### Scenario: 关闭开关导出

- **WHEN** 动画开关关闭
- **THEN** 清单 `schemaVersion` 仍为 2，但所有页 `layers` 为空、`transition.type` 为 `none`
- **AND** 解出的页面图与 M6-1 产物逐像素相同

### Requirement: 动画能力缺失时降级为可播放

播放器在缺 `Element.animate`、`prefers-reduced-motion: reduce`、清单版本高于支持版本三种情况下 MUST 仍可播放，MUST NOT 抛错或停在封面。

`prefers-reduced-motion` 下 MUST 保留 opacity 变化、去掉位移与缩放，且**时长不变**——缩短时长会让动画提前结束、与音频和字幕错位。

#### Scenario: 环境无 WAAPI

- **WHEN** `Element.prototype.animate` 不存在
- **THEN** 播放器按静态切页播放完整时长，不抛异常

#### Scenario: 清单版本过高

- **WHEN** 读到 `schemaVersion` 大于播放器支持的最大版本
- **THEN** 播放器明确提示「需要更新播放器」，而不是按旧版逻辑猜着播

### Requirement: 页内镜头切片写入清单

声明了页内镜头的页，清单 MUST 写出 `slides[i].shots[]`，每项含 `index`、`image`、`crop?`、`startMs`、`durationMs`、`crossFadeMs`，语义与 IR 的 `render.Shot` 同构。首镜 MUST 复用该页的页级图（同一份字节、同一个包内路径），MUST NOT 为它多写一个文件；第 2 镜起 MUST 写成独立文件（`slides/NNN-shot-0K.png`）。

#### Scenario: 首镜复用页级图

- **WHEN** 一页声明两镜，首镜 `image` 等于 `slides[i].image`
- **THEN** 包内 MUST 只有一个与页级图对应的文件，`shots[0].image` MUST 是它的路径

#### Scenario: 首镜不等于页级图

- **WHEN** 清单里 `shots[0].image` 与 `slides[i].image` 不同
- **THEN** 校验 MUST 失败并指出两者，MUST NOT 让「这一页长什么样」有两个说法

### Requirement: 清单的镜头校验独立于 IR 校验

产物侧 MUST 独立于 IR 再校验一遍：首镜 `image` 等于 `slide.image`、`index` 等于位置、时间轴首尾相接且总长等于页时长（±1 帧）、`crossFadeMs` 短于本镜、`crop` 在原页范围内、每个 `shot.image` 在包内存在。

重复一遍不是冗余：门禁对的是**产物**——清单可能被手改，也可能由别的工具生成，不能假设它一定出自本仓的编译器。

#### Scenario: 手改清单删掉一镜

- **WHEN** 手工把 `shots` 里中间一镜删掉，留下时间空洞
- **THEN** 校验 MUST 失败并指出页码与缺失区间，MUST NOT 让包在「少一镜」的状态下通过

### Requirement: 裁切比例只能在打包期核

`crop` 的宽高比与 `image` 的像素宽高比之差 MUST ≤1%。这条 MUST 放在打包期：IR 里 `image` 只是路径，量不出像素尺寸；图落盘之后才能量。

比例算错时浏览器会把图拉伸，而拉伸后的画面「看着还挺正常」——所以它必须在能读到图的那一层拦下。

#### Scenario: 裁切比例算错

- **WHEN** `crop` 是 640×360（16:9），而落盘的 `image` 是 640×480（4:3）
- **THEN** 打包 MUST 失败并报出页码与镜号，MUST NOT 写出会被浏览器拉伸的包

### Requirement: 引入 shots 不升 schemaVersion

`schemaVersion` MUST NOT 因为引入 `shots` 而升高。依据是证据不是直觉：播放器不读这个字段，升版本只会让 Go 的校验器拒绝旧包，客户端行为一点没变。

`shots` 是可选字段，旧播放器会忽略它、停在首镜图（`slide.image` 已由校验绑死为 `shots[0].image`）。所以真实表现是「降级可播放、但不换镜」——规格 MUST 如实这么写，MUST NOT 声称「已兼容」。

#### Scenario: 旧播放器读到 shots

- **WHEN** 用引入镜头之前的播放器打开一份带 `shots` 的包
- **THEN** 包 MUST 仍可播放，画面停在首镜图；这不是兼容，是降级可播放

### Requirement: 镜头相机字段为可选且不升 schemaVersion

声明了页内镜头的页，`slides[i].shots[]` 的每一项 MAY 另带 `camera[]`（相机关键帧），语义与
IR 的 `render.CameraKeyframe` 同构，字段为 `atMs`、`x`、`y`、`scale`。`camera` 是可选字段：
缺省时产物 MUST 与引入它之前逐字节一致。

`camera` 非空时，其所属镜头 MUST 同时声明 `crop`（相机在特写图内部运动）。打包 MUST 校验
`scale ≥ 1` 与中心落在覆盖安全区间 `[0.5/scale, 1-0.5/scale]`、关键帧按 `atMs` 升序；任一违例
MUST 记录诊断并拒绝该镜头。（注：`[0,1]` 只是归一化存储范围，并非安全区间。）

`schemaVersion` MUST NOT 因为引入 `camera` 而升高：旧播放器忽略 `camera` 字段，停在首镜图
（`slides[i].shots[0].image` 已由校验绑死为 `slides[i].image`），真实表现为「降级可播放、但不运
动」——规格 MUST 如实这么写，MUST NOT 声称「已兼容运动」。

#### Scenario: 无相机的包逐字节一致

- **WHEN** 用一份不含 `camera` 的既有包重新打包
- **THEN** 产物 MUST 不含 `camera` 字段，且 MUST 与引入本需求前逐字节一致

#### Scenario: 带相机的镜头写出 camera 字段

- **WHEN** 清单里某镜头声明了 `camera`
- **THEN** 包内 `shots[k]` MUST 含 `camera[]`，每项含 `atMs`/`x`/`y`/`scale`，且 `crop` 非空

#### Scenario: 相机镜头缺 crop 被拒

- **WHEN** 某镜头含 `camera` 但 `crop` 为空
- **THEN** 打包 MUST 记录诊断并拒绝该镜头，MUST NOT 产出无特写的相机镜头

#### Scenario: 引入 camera 不升 schemaVersion

- **WHEN** 包内含 `camera` 字段
- **THEN** `schemaVersion` MUST 保持不变，旧播放器忽略 `camera` 停在首镜且不报错
