# Spec Delta

## Purpose

定义渲染服务的运行时与镜像契约：什么由合成器画、什么由 worker 做（转场与元素动画同批合成）、镜像里必须固定什么（浏览器/字体）、像素级约束（去残影、编码参数）、以及不可用时如何降级。目标是让「同一份 IR 在任何一台渲染机上出同样的片段」这件事可验证，而不是靠部署时口头约定。

## ADDED Requirements

### Requirement: 转场与元素动画共用一次合成

页转场（`transition.type ∈ {fade, slide, zoom}`）MUST 由合成器在**入场页开头**绘制前一页末帧，占该页前 `transition.durationMs`，MUST NOT 改变任何一页的时长。

worker MUST NOT 另建一条 ffmpeg 交叉淡化（`xfade`）路径：`xfade` 是重叠语义，会让成片变短，与本条的时间约定冲突，两套时序并存必然对不齐。片段缓存的键也因此把「前一页」纳入（`openspec/specs/render-plan/spec.md`）。

整份导出不含任何元素级动画（`layers[*].clips` 全空）时 MUST NOT 提交渲染服务——此时转场也不产出，输出为硬切（已知限制，见 tasks 7.1）。

#### Scenario: 只有页转场

- **WHEN** 计划中所有页的 `layers` 均为空，仅有 `transition`
- **THEN** worker 不发起任何 `/v1/render` 请求，输出为硬切（转场不产出）
- **AND** 该限制 MUST 在 tasks 7.1 有明确记录，MUST NOT 让用户以为「转场已生效」

#### Scenario: 有元素级动画

- **WHEN** 任一页存在带 clip 的图层
- **THEN** worker 提交完整 IR 到渲染服务，并等待逐页片段，转场在同一批片段里由合成器绘制

### Requirement: 浏览器版本固定且可读

镜像 MUST 使用固定版本的 `chrome-headless-shell`，版本号 MUST 由单一事实源（`deploy/renderer/CHROME_VERSION`）决定并在构建期校验下载包摘要。运行期 MUST NOT 尝试联网下载浏览器。

`GET /healthz` MUST 返回 `chromeVersion`，且该值 MUST 与构建期固定版本一致。

#### Scenario: 两次构建同一版本

- **WHEN** 用同一 `CHROME_VERSION` 构建两次镜像
- **THEN** 两个镜像的 `/healthz.chromeVersion` 相同，同一 IR 的 `planHash` 相同

#### Scenario: 容器无外网

- **WHEN** 容器以 `network: none` 启动并收到渲染请求
- **THEN** 渲染正常完成，日志中 MUST NOT 出现浏览器下载尝试

### Requirement: 渲染器版本参与 planHash

`rendererVersion` MUST 形如 `<backend>@<version>|chrome@<version>`，并 MUST 参与 `planHash`。`/healthz.version` MUST 返回同一字符串。

`schemaVersion` 不符 MUST 拒绝调用（正确性问题）；`version` 不符 MUST NOT 拒绝（由 `planHash` 吸收，避免滚动升级期间导出失败）。

#### Scenario: 只升级浏览器

- **WHEN** 同一份 IR 在 Chrome 升级前后各渲染一次
- **THEN** 两次 `planHash` 不同，缓存未命中并重渲，而不是返回旧像素

#### Scenario: 滚动升级期间新旧实例并存

- **WHEN** 客户端连到 `version` 不同但 `schemaVersion` 相同的实例
- **THEN** 请求正常处理，仅缓存键不同

### Requirement: 镜像只含可再分发字体

镜像 MUST 只内置 SIL OFL 许可的字体（默认 Noto Sans SC）。MUST NOT 内置微软雅黑等不可再分发字体。

`/healthz.fonts` MUST 列出 `{family, version}`；字体版本 MUST 参与 `planHash`。缺失必需字体时客户端 MUST 返回 `ErrFontUnavailable`，MUST NOT 产出方框字。

#### Scenario: 租户要求雅黑

- **WHEN** 计划的 `fonts` 含 `Microsoft YaHei` 而镜像未提供
- **THEN** 客户端在提交前返回 `ErrFontUnavailable`，提示改用本地模式

### Requirement: 运行时最小权限

渲染容器 MUST 以非 root 用户运行、rootfs 只读、工作目录为 tmpfs、无外网出口，并 MUST 设置 CPU/内存/PID 限额。服务内并发 MUST 有上限。

#### Scenario: 容器被投喂恶意计划

- **WHEN** 计划含绝对路径或 `..` 逃逸
- **THEN** 服务返回 `unsafe_path`，MUST NOT 读取工作目录之外的文件

### Requirement: 元素级动画必须先去残影（底板）

当一页存在 `elementId` 非空的图层时，backend MUST 在合成前生成底板：把每个图层 Box 用「盒外采样环的逐通道中位色」填平，并以 `page.plateImage` 交给合成器。图层素材 MUST 取自原图。

`plateImage` 缺省时合成器 MUST 退回 `baseImage`（等价于旧行为），且 backend MUST 记录 warning，MUST NOT 静默产出双影画面。采样环颜色极差超过阈值时 MUST 记录 `plate_patch_noisy`（该页补丁可能可见）。

#### Scenario: 标题滑入

- **WHEN** 一页的标题区域有 `slideIn`，底图为纯色
- **THEN** 底板中该区域为周围底色，动画期间目标位置不出现静态副本

#### Scenario: 照片底图

- **WHEN** 底图是照片，元素区域周围颜色差异超过阈值
- **THEN** 补丁照常生成，同时记录 `plate_patch_noisy` 警告

### Requirement: 嵌套图层先抹掉子元素再抬升

当某图层的 Box **包住**另一个动画元素时，backend MUST 为该层生成「已抹掉这些子元素」的位图并以 `layers[i].image` 交给合成器。抹除用的填充色 MUST 取自**原图**的盒外环，MUST NOT 取自底板（底板按图层顺序递进填充，取它会得到上一层被抹后的颜色）。

合成器见到 `image` MUST 直出该位图、MUST NOT 再做偏移裁切；`image` 缺省时 MUST 退回「从 `baseImage` 按 Box 偏移裁切」。合成器 MUST 先画父层、后画子层（父层的子元素已被抹掉，顺序反了会把子元素盖掉）。

> **边界**：只处理「包含」关系。两个部分重叠、互不包含的兄弟元素仍会互相带出——
> 单张栅格无法推断逐元素 alpha，要彻底解决需上游提供分层素材（tasks 5.9）。

#### Scenario: 卡片内的要点依次入场

- **WHEN** 一张卡片淡入（`startMs=260`），卡片盒内有 3 条 bullet 分别在 `700/920/1140ms` 淡入
- **THEN** 卡片素材里不含这 3 条 bullet（被填成卡片底色），三条 bullet 各自按自己的起点出现
- **AND** 卡片素材的填充色等于卡片底色，不是白色（取色取自原图而非底板）

### Requirement: 片段编码参数固定

渲染服务返回的每个片段 MUST 为 H.264（sample entry `avc1`）/ `yuv420p` / 只有视频轨。第一个样本 MUST 是关键帧；相邻关键帧的间隔 MUST ≤ 30 帧，「最后一个关键帧到片尾」那一段同样计入间隔。

间隔 MUST NOT 被断言为恰好 30：x264 默认会在场景切换处插入更密的关键帧，变短是正常编码行为。契约只约束上限——变长会让按页 seek 变慢，也会让拼接处的首帧落在 P 帧上。

片段帧数 MUST 等于 `round(durationMs × fps / 1000)`（至少 1 帧）。

#### Scenario: 片段被 `-c copy` 拼接

- **WHEN** worker 用 concat demuxer 拼接全部片段
- **THEN** 拼接 MUST 不触发重编码，且输出时长等于各页 `durationMs` 之和（误差 ≤ 半帧/页）

#### Scenario: 合成器的编码参数被改坏

- **WHEN** `pixelFormat` 被改成 `yuv444p`，或 `gopSize` 被放大到超过页长
- **THEN** 镜像自检 MUST 以非零退出码结束，并报出实测值与期望值（`chroma = yuv444p, want yuv420p` / `keyframe gap = 45 frames, want ≤30`）

### Requirement: 镜像自带可执行自检

镜像 MUST 提供自检入口：用固定样例渲染至少 2 页，校验产物存在、为合法 MP4 容器，且编码参数符合「片段编码参数固定」。自检 MUST NOT 依赖 ffprobe 等镜像外的二进制——运行时镜像里没有 ffmpeg，靠它校验等于把门禁建在镜像之外。自检失败 MUST 以非零退出码结束，并在失败信息里带上实测值。部署流水线 MUST 在发布前执行自检。

#### Scenario: 浏览器缺失

- **WHEN** 镜像内浏览器路径错误
- **THEN** 自检以非零退出码失败并指出缺失的路径，而不是发布一个「构建成功但渲染必失败」的镜像

### Requirement: 渲染不可用不得阻断导出

渲染服务不可用、超时或单页失败时，导出 MUST 继续，并按 M2 D7 的降级矩阵处理：整片回退静态版；单页回退该页静态图。MUST NOT 因为动画失败让用户拿不到任何产物。

#### Scenario: 服务未部署

- **WHEN** `animation.backend = none` 或 `/healthz` 不可达
- **THEN** 导出走静态流程并提示「动画服务暂不可用，已导出静态版本」
