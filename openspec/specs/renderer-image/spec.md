# renderer-image Specification

## Purpose
定义渲染服务的运行时与镜像契约：什么由合成器画、什么由 worker 做（转场与元素动画同批合成）、镜像里必须固定什么（浏览器/字体）、像素级约束（去残影、编码参数）、以及不可用时如何降级。目标是让「同一份 IR 在任何一台渲染机上出同样的片段」这件事可验证，而不是靠部署时口头约定。

## Requirements

### Requirement: 转场与元素动画共用一次合成

页转场（`transition.type ∈ {fade, slide, zoom}`）MUST 由合成器在**入场页开头**绘制前一页末帧，占该页前 `transition.durationMs`，MUST NOT 改变任何一页的时长。

worker MUST NOT 另建一条 ffmpeg 交叉淡化（`xfade`）路径：`xfade` 是重叠语义，会让成片变短，与本条的时间约定冲突，两套时序并存必然对不齐。片段缓存的键也因此把「前一页」纳入（`openspec/specs/render-plan/spec.md`）。

转场不是独立通道：它没有自己的渲染提交路径，只随同一批片段由合成器绘制。因此渲染服务不可用时（`animation.backend = none` 或 `/healthz` 不可达），转场一并不产出，输出为硬切。这是 ADR-1 的已知代价，已接受（tasks 7.1 决策①）。

#### Scenario: 渲染服务不可用且有转场

- **WHEN** 时间轴里有页配置了 `transition`，而 `animation.backend = none` 或 `/healthz` 不可达
- **THEN** 导出走静态拼接，页与页之间为硬切（转场不产出）
- **AND** 导出选项 MUST 在用户可见处说明「静态版不含页间转场」，MUST NOT 让用户以为「转场已生效」

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

> **边界**：只处理「包含」关系。两个部分重叠、互不包含的兄弟元素仍会互相带出（单张栅格
> 无法推断逐元素 alpha），按下面「部分重叠的兄弟元素」这条单独约束。

#### Scenario: 卡片内的要点依次入场

- **WHEN** 一张卡片淡入（`startMs=260`），卡片盒内有 3 条 bullet 分别在 `700/920/1140ms` 淡入
- **THEN** 卡片素材里不含这 3 条 bullet（被填成卡片底色），三条 bullet 各自按自己的起点出现
- **AND** 卡片素材的填充色等于卡片底色，不是白色（取色取自原图而非底板）

### Requirement: 部分重叠的兄弟元素必须可被告知

两个图层的 Box 互不包含、但重叠面积 ≥ 5% 时，backend MUST 在编译期产出 `sibling_overlap_partial` 诊断（带第一对元素 id），MUST NOT 静默出片。这是已知限制、不是待修缺陷：单张栅格推不出逐元素 alpha，根除需上游按元素分别出图，超出本服务契约。

重叠面积低于 5% 的边距级交叠 MUST NOT 报——文本框包围盒普遍带内边距，纯几何判定会天天误报，告警随即失去意义。成片质量门禁 MUST NOT 把这类残影记为回归。

#### Scenario: 两个兄弟元素部分重叠

- **WHEN** 同页两个图层的 Box 互不包含、重叠面积 ≥ 5%
- **THEN** 编译期产出 `sibling_overlap_partial` 诊断并带上第一对元素 id，任务仍可继续
- **AND** 导出结果 MAY 出现两者互相带出的残影；这是已知边界，不按回归处理

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

自检样例 MUST 覆盖页内切镜：每页至少两镜，第二镜带 `crop`。镜头图是 staging 的派生物，缺了不会报错——合成器读不到就整页退回静态画面，成片「看着正常、切镜全黑」。断言 MUST 按 `crop` 与页图实际倍率算出期望像素尺寸，MUST NOT 只查文件存在：文件在、尺寸错，观众只看到一张放大得不合适的图，没有任何报错。

#### Scenario: 浏览器缺失

- **WHEN** 镜像内浏览器路径错误
- **THEN** 自检以非零退出码失败并指出缺失的路径，而不是发布一个「构建成功但渲染必失败」的镜像

#### Scenario: 切镜图未被派生

- **WHEN** staging 不再从页图裁出特写（例如 `stageShotAssets` 早退）
- **THEN** 自检 MUST 以非零退出码失败（实测报 `all 2 pages failed`：合成器读不到图），MUST NOT 产出一段「无切镜但声称成功」的成片

#### Scenario: 切镜图尺寸不对

- **WHEN** 页图是画布的 2 倍，而镜头图按 1× 裁出
- **THEN** 自检 MUST 报出实测与期望尺寸（`尺寸 560x160, want 1120x320`）

### Requirement: 构建期校验镜像内源码闭合

镜像的源码来自 Dockerfile 里的白名单 `COPY`，因此「仓库里有、镜像里没有」是一个构建成功也发现不了的缺陷类别。构建 MUST 在编译产物之后、切到非 root 之前执行两道检查，任一失败即让构建失败：

- **入口的相对导入闭合**：静态解析 `render.mjs` / `still.mjs` 的 `from './x'`，逐个确认目标存在。MUST NOT 用真实 `import()` 验证——入口顶层会直接执行渲染。
- **真打一次包**：调 `@remotion/bundler` 的 `bundle()`，覆盖 `src/*.tsx` 的跨目录依赖（静态解析看不到这些）。它只用 webpack，不启动浏览器。

两道检查都必须放在 `/app` 下：Node 的包解析是按**文件位置**向上找 `node_modules`，放到 `/tmp` 会以「模块不存在」失败，看起来像检查自己坏了。

#### Scenario: 入口依赖漏拷

- **WHEN** `render.mjs` 引用的 `degrade.mjs` 没有被 `COPY` 进镜像
- **THEN** 构建 MUST 在「入口的相对导入闭合」一步失败并报 `missing import ./degrade.mjs`

#### Scenario: 合成器依赖漏拷

- **WHEN** `renderer/src/*.tsx` 引用的仓库根 `shared/` 没有被 `COPY` 进镜像
- **THEN** 构建 MUST 在「真打一次包」一步失败，并透出 webpack 的解析错误（`Can't resolve '/shared/motion'`）

### Requirement: 渲染不可用不得阻断导出

渲染服务不可用、超时或单页失败时，导出 MUST 继续，并按 M2 D7 的降级矩阵处理：整片回退静态版；单页回退该页静态图。MUST NOT 因为动画失败让用户拿不到任何产物。

页间转场与元素动画共用同一次合成（ADR-1），因此动画通道整体不可用时转场一并失效，输出为硬切。这是已知且被接受的代价：导出选项 MUST 在用户可见处说明「静态版不含转场」，MUST NOT 只让用户在成片里发现页与页之间突然硬切。

#### Scenario: 服务未部署

- **WHEN** `animation.backend = none` 或 `/healthz` 不可达
- **THEN** 导出走静态流程并提示「动画服务暂不可用，已导出静态版本」

#### Scenario: 用户以为转场还在

- **WHEN** 部署未配置渲染服务（`renderer_not_configured`），用户打开 MP4 导出对话框
- **THEN** 动画选项旁 MUST 出现一句说明：静态版不含页间转场，页与页之间为硬切
- **AND** 该说明由 `web/animation-check.mjs` 断言（文案被删或改掉「转场」即红）
