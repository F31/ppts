# render-plan Specification

## Purpose
定义目标无关的渲染输入 `CompiledTimeline`（IR）、规格到 IR 的编译规则、渲染服务契约与客户端重试语义。IR 是 MP4 与 HTML 两个后端的唯一事实源：它只携带可插值数值，后端不解释动效名。

## Requirements

### Requirement: IR 不含动效名，只含可插值数值

`CompiledTimeline` MUST 是目标无关的渲染输入。每个动效 MUST 已被展开为「属性 → 起点值/终点值」加一条 `cubic-bezier` 缓动；属性名 MUST 限定在 `opacity`、`translateX`、`translateY`、`scale` 之内。

规范 MUST NOT 要求渲染后端识别动效名、参数名或讲稿语义。`motion.effect` 字段 MUST 仅作审计留痕保留，渲染后端 MUST NOT 读它来决定行为。

#### Scenario: 同一份 IR 被两个后端插值

- **WHEN** 同一份 IR 交给两个独立后端（帧渲染器 / DOM 编译器）
- **THEN** 两者在任意时刻 t 的画面差异只来自渲染精度，不来自动效解释

#### Scenario: 新增动效不需要改后端

- **WHEN** catalog 新增一种入场动效
- **THEN** 只需在 `internal/animation/plan.go` 增加该动效的属性展开，IR 结构与后端零改动

### Requirement: 几何换算只做一次

页面盒 MUST 在编译期由 EMU 换算成目标画布像素，且 MUST 落在 `[0, Width] × [0, Height]` 内。换算 MUST 使用调色板统一比例（`px = emu × canvas / slideEMU`），不得对 X/Y 分别使用不同比例。

#### Scenario: 越界盒被钳制

- **WHEN** 元素 `Bounds` 超出源页范围（解析器给出了异常几何）
- **THEN** 编译器钳制到画布内，并记录 `box_clamped` 诊断；MUST NOT 静默输出负宽高

#### Scenario: 无几何元素被跳过

- **WHEN** 轨道对应的元素在页内不存在或没有 `Bounds`
- **THEN** 该轨道被丢弃并记录 `anchor_missing` 诊断；其余轨道 MUST 照常编译

### Requirement: 时间在编译期解析为页内绝对毫秒

`withSegment:<id>` MUST 被解析为该分段相对本页起点的毫秒偏移；`atTime:<ms>` MUST 直接取值；`onEnter` MUST 为 0。解析后 MUST 满足 `0 ≤ startMs` 且 `startMs + durationMs ≤ pageDurationMs`。

无法解析的分段 MUST 记录 `segment_unresolved` 诊断并把该 clip 的起点退化为 0，MUST NOT 静默挪到页尾或丢弃整页。

#### Scenario: 分段锚定落在页中

- **WHEN** 某页从全局 12000ms 开始，第 3 段从全局 15500ms 开始，clip 触发为 `withSegment:seg-3`
- **THEN** IR 中该 clip 的 `startMs` 为 3500

#### Scenario: 锚定失效

- **WHEN** `withSegment:seg-9` 在当前讲稿修订中不存在
- **THEN** 该 clip 的 `startMs` 为 0，IR 带 `segment_unresolved` 诊断，页面仍可渲染

### Requirement: 强度只缩放幅度

编译期 MUST 用 `intensity` 线性缩放位移量与缩放幅度（`scale` 的 `1-from` 差值），MUST NOT 改变 `durationMs`。

#### Scenario: 强度不改时长

- **WHEN** spec 中 clip 为 `slideIn`、`durationMs=600`、`offset=40`，页 `intensity=1.5`
- **THEN** IR 中 `durationMs` 仍为 600，`translateY.from` 为 60

### Requirement: 页转场由预设决定且不进入可编辑规格

每页 MUST 携带 `transition{type,durationMs}`。首屏 MUST 为 `none`（没有前一页可交叉）。转场类型 MUST 限定在 `none|fade|slide|zoom`，时长 MUST ≤ 400ms（§8.4 桥接片段上限）。

#### Scenario: 预设决定转场

- **WHEN** 页规格 `presetId=tech-dynamic`
- **THEN** 非首页 `transition.type=slide`，`durationMs` 取自预设并 ≤ 400

### Requirement: IR 是幂等的规范化字节

`planHash` MUST 为规范化 JSON 的 SHA-256，计算 MUST 覆盖 `schemaVersion`、画布、fps、字体版本、每页时长与全部运动数值；MUST NOT 覆盖任何时间戳、路径前缀、job id。

#### Scenario: 两次编译同一规格

- **WHEN** 同一 spec、同一几何、同一时间轴编译两次
- **THEN** 两次 `planHash` 相同，字节级一致（不含 `warnings` 以外的易变字段）

#### Scenario: 改一个像素就换 hash

- **WHEN** 仅把某元素盒宽改 1px
- **THEN** `planHash` 改变

### Requirement: 渲染服务无状态且幂等

服务 MUST 提供 `POST /v1/render`（返回 `jobId`）、`GET /v1/render/{jobId}`、`DELETE /v1/render/{jobId}`、`GET /healthz`。相同 `planHash` 的重复提交 MUST 返回同一 `jobId` 且不重复渲染，**唯一例外是已有 job 落在 `failed` 或 `canceled` 终态**。

失败与取消表示「这次没成」，MUST NOT 让该 `planHash` 永久不可渲染：重试 MUST 重新渲染，并 MUST 分配新的 `jobId`（重试需要独立的工作目录，不能与被取消那一轮残留的清理抢同一路径）。旧 `jobId` MUST 仍可查询，并保持其终态与错误信息不变。

#### Scenario: 重复提交

- **WHEN** 同一 plan 连续提交两次
- **THEN** 两次返回同一 `jobId`，backend 只被调用一次

#### Scenario: 失败后重试

- **WHEN** 某份计划渲染失败（瞬时故障），调用方用完全相同的 plan 再次提交
- **THEN** 服务 MUST 重新渲染而不是复用失败的 job，返回一个新的 `jobId`；旧 `jobId` 查到的仍是 `failed`

#### Scenario: 取消后重试

- **WHEN** 任务被 `DELETE` 取消后，调用方用完全相同的 plan 再次提交
- **THEN** 服务 MUST 重新渲染并返回新的 `jobId`，产物 MUST 落在新目录

#### Scenario: 已成功后重复提交

- **WHEN** 任务已 `succeeded`，调用方再次提交同一 plan
- **THEN** 返回同一 `jobId`，MUST NOT 重新渲染（幂等只服务成功结果）

#### Scenario: 非法 plan

- **WHEN** plan 未通过 IR 校验
- **THEN** 返回 400 与具体字段路径，MUST NOT 创建 job

### Requirement: 产物可下载，且可见性只有一份

服务 MUST 提供 `GET /v1/render/{jobId}/artifacts/{name}`，把 `JobStatus.Artifacts` 列出的产物原样返回（含 `Content-Length`，支持 `Range`）。

可下载集合 MUST 与 `JobStatus.Artifacts` 一致：清单里有而磁盘上没了的 MUST 返回 404（与 TTL 到期同等对待，调用方据此降级而非重试）；不在清单里的文件（清单拍完之后才出现的残留）MUST 返回 404，MUST NOT 因为「文件在工作目录里存在」就提供下载。

路径 MUST 经越界校验：绝对路径、`..`、反斜杠一律 400 `unsafe_path`，且该判定 MUST 先于清单查询——否则攻击尝试与打错名字在日志里无法区分。

#### Scenario: 按清单下载

- **WHEN** 任务已 `succeeded`，调用方按 `Artifacts` 中的名字逐个下载
- **THEN** 每个名字都返回 200 与完整字节；一个都不缺、字节数等于 `Content-Length`

#### Scenario: 下载未列出的文件

- **WHEN** 调用方请求工作目录里存在、但不在 `Artifacts` 里的文件（例如终态后才写入的残留）
- **THEN** 返回 404，不返回任何内容字节

#### Scenario: 越界路径

- **WHEN** 请求的 `name` 含 `..` 或为绝对路径
- **THEN** 返回 400 `unsafe_path`，且不触碰工作目录之外的文件

#### Scenario: 清单还在但文件已删

- **WHEN** `Artifacts` 里仍列着某文件，而磁盘上已被删除
- **THEN** 返回 404（不是 500）：对调用方而言与 TTL 到期一致

### Requirement: 容量有上限，超限背压而不排队

服务 MUST 为「已受理、尚未起跑」的任务数设上限（`MaxQueued`，默认 8），并 MUST 让排队期持有的计划数不超过该上限。

达到上限时，`POST /v1/render` MUST 返回 `429`、`Retry-After` 头与错误码 `queue_full`，且 MUST NOT 创建 job、MUST NOT 消耗该计划的重试编号。判定 SHOULD 发生在读取请求体之前。

排队中的任务被取消时 MUST 立即释放其计划引用，MUST NOT 等信号量轮到它。`/healthz` 的 `running` MUST 只统计真正在渲染的任务，排队数 MUST 由 `queued` 单独暴露。

#### Scenario: 队列打满

- **WHEN** 并发上限已占满，排队数达到 `MaxQueued`，又来一个新计划
- **THEN** 返回 429 + `Retry-After`，错误码 `queue_full`，不创建 job；队列消化后重新可受理

#### Scenario: 排队中被取消

- **WHEN** 一个仍处于 `queued` 的任务被 `DELETE`
- **THEN** 其计划的引用立即释放，任务落 `canceled` 且仍可查询

### Requirement: 终态任务有过期时间

服务 MUST 为终态任务设保留时长（`JobTTL`，默认 30 分钟），**从任务落终态的时刻起算**，不得从受理时刻起算。过期后服务 MUST 删除该 job 记录、释放其 `planHash` 映射、删除其产物目录；此后 `GET /v1/render/{jobId}` MUST 返回 404。

调用方 MUST 在保留时长内取走产物。保留时长与回收节奏 MUST 可配置。

#### Scenario: TTL 内可查

- **WHEN** 任务已终态但未超过保留时长
- **THEN** 状态与产物仍可获取

#### Scenario: 过期回收

- **WHEN** 终态任务超过保留时长
- **THEN** 记录、`planHash` 映射与产物目录一并消失，`GET` 返回 404；同一计划再次提交会重新渲染

#### Scenario: 长任务不被误回收

- **WHEN** 一次渲染耗时接近渲染超时上限，成功后才开始计时
- **THEN** 它不会因「受理后过久」而被立刻回收

### Requirement: 安全边界可验证

服务 MUST 拒绝工作根目录之外的 `baseImage` 路径（含 `..`、绝对路径、盘符）。MUST 限制请求体大小与页数/帧数上限。

#### Scenario: 路径逃逸

- **WHEN** `baseImage` 为 `../../etc/passwd` 或 `C:\Windows\win.ini`
- **THEN** 返回 400，错误码 `unsafe_path`

### Requirement: 客户端区分可重试与永久失败

客户端 MUST 把 4xx（除 408/429）归为永久失败并返回带字段路径的错误；5xx/408/429/超时 MUST 按指数退避重试 2 次。服务不可达或 `/healthz` 失败 MUST 返回 `ErrAnimationUnavailable`，供上层降级到静态导出。

#### Scenario: 服务未部署

- **WHEN** 渲染服务地址不可达
- **THEN** 客户端返回 `ErrAnimationUnavailable`，上层据此回退静态路径且不向用户报错

#### Scenario: 校验失败不重试

- **WHEN** 服务返回 400
- **THEN** 客户端不重试，直接返回错误

### Requirement: 片段缓存以页为粒度，键覆盖所有影响像素的输入

服务 MUST 支持可选的片段缓存（`SegmentCache`）。缓存键 MUST 是「渲染这一页所需全部输入」的规范化 SHA-256，至少覆盖：

- 本页：`durationMs`、`baseImage`（按 basename）、`layers` 的 `elementId/kind/box/clips`、`transition`；
- 前一页（仅当本页 `transition.type != none` 且 `durationMs > 0`）：`durationMs`、`baseImage`、`layers`——转场在该页开头绘制前一页的末帧，前一页变了这一页的开头就变了；
- 渲染环境：IR 版本、画布宽高、fps、底色、字体版本、渲染器版本、编码参数（CRF/GOP）、浏览器版本。

缓存键 MUST NOT 包含与像素无关的量：页序号、`slideId`、`plateImage`、`layer.image`、`warnings`。前四项分别随位置/命名/派生路径变化，包含它们会让「同一页换个位置」无谓地失效；后两者是确定性派生物，包含它们会让「换一个工作目录」命中不了。

未配置缓存时 MUST 保持原有行为（每页都渲）。

#### Scenario: 改一页只失效相邻页

- **WHEN** 一份 10 页计划，只修改第 5 页（0 基）的 `clips`
- **THEN** 第 5 页与其后继第 6 页（若其转场生效）MUST 未命中，其余 8 页 MUST 命中

#### Scenario: 页序号不参与键

- **WHEN** 把一份计划的第 0 页原样挪到第 5 位（内容与时长不变、两侧转场相同）
- **THEN** 该页的缓存键 MUST 不变

#### Scenario: 前一页变化使转场页失效

- **WHEN** 第 3 页内容改变，第 4 页的 `transition.type` 为 `fade`
- **THEN** 第 4 页 MUST 未命中（它的开头几帧画的是第 3 页末帧）

#### Scenario: 关闭缓存

- **WHEN** 服务未配置 `SegmentCache`
- **THEN** 每一页 MUST 都交给 backend 渲染，行为与引入缓存前一致

### Requirement: 命中页不渲染，未命中页只渲染自己的帧区间

服务 MUST 在提交给 backend 之前完成缓存查询，并把**未命中页集合**作为渲染范围交给 backend。backend 支持的页级渲染接口 MUST 只对这集合内的页调用渲染器，MUST NOT 顺带渲染命中页。

staging（底图复制、底板与层图生成）MUST 只对「未命中页 ∪ 其转场依赖的前一页」执行；全部命中时 MUST 完全跳过 staging 与渲染器调用。

进度 MUST 反映真实工作量：起跑时的 `done` MUST 等于命中页数，`total` MUST 等于总页数。

#### Scenario: 全命中

- **WHEN** 同一份计划在一个新进程里再次提交（缓存目录共享）
- **THEN** backend MUST 不被调用，产物目录里的片段全部来自缓存，`JobStatus.Progress` 从 `done=total` 直接进入 `succeeded`

#### Scenario: 部分命中

- **WHEN** 10 页计划中 8 页命中
- **THEN** backend MUST 只收到 2 页的渲染范围；进度首个回报 MUST 是 `done=8,total=10`

#### Scenario: staging 不越界

- **WHEN** 10 页计划中只有第 5 页未命中，且第 5 页需要底板
- **THEN** 服务 MUST 只为第 5 页（及其转场依赖的第 4 页）复制底图与生成底板；MUST NOT 因「计划里有 10 页」而 staging 全部 10 页

### Requirement: 命中产物与冷渲染逐字节相同

命中页的片段 MUST 与「同一份计划全新渲染」产出的同页片段逐字节相同；成片 MUST 与冷渲染成片具有相同的容器参数、时长与帧数。

#### Scenario: 字节一致

- **WHEN** 对同一份 10 页计划分别做「冷渲染」与「全命中渲染」
- **THEN** 两次的每个 `segment_NNN.mp4` 的 SHA-256 MUST 相同；成片 MP4 的时长与帧数 MUST 相同

#### Scenario: 编辑后重渲不串页

- **WHEN** 只改第 5 页后重渲，其余页来自缓存
- **THEN** 成片第 5 页的画面 MUST 是新内容，其余页 MUST 与上一版逐帧相同（不出现「缓存错位到别的页」）

### Requirement: 缓存不具备让渲染失败的能力

读缓存出错（文件损坏、截断、读失败）MUST 视为未命中并重新渲染，MUST NOT 让任务失败。写缓存出错 MUST 只记日志，MUST NOT 影响任务终态。

缓存项在复用前 MUST 通过最小容器校验（存在、≥1 KiB、第 4–8 字节为 `ftyp`）；校验失败的项 MUST 被视为未命中并被替换。

#### Scenario: 截断的缓存项

- **WHEN** 缓存里某个片段被截断成 10 字节
- **THEN** 该页 MUST 未命中并重新渲染，任务终态 MUST 为 `succeeded`

#### Scenario: 缓存目录不可写

- **WHEN** 缓存目录只读
- **THEN** 渲染 MUST 正常完成，只多出写失败的日志；`JobStatus` 的命中/实渲计数 MUST 仍如实反映本次行为

### Requirement: 降级页不写入缓存

被渲染器降级的页（`page_render_failed` / `page_skipped`）MUST NOT 写入缓存。降级是瞬时故障的表现，缓存它会让一次偶发失败固化成「这一页永远不动」。

#### Scenario: 注入失败后不缓存

- **WHEN** 某页渲染失败并被替换为静态画面
- **THEN** 该页 MUST NOT 出现在缓存里；同一份计划再渲一次，MUST 重新尝试渲染该页

### Requirement: 命中与实渲数量可观测

`GET /v1/render/{jobId}` 的响应 MUST 报出本次渲染的缓存命中页数与实渲页数（两个整数，缺省为 0）。两者之和 MUST 等于计划页数（不含 `page_skipped` 的解释：被跳过的页记入实渲，因为它们确实尝试过）。

#### Scenario: 计数正确

- **WHEN** 10 页计划中 8 页命中、2 页实渲
- **THEN** 响应 MUST 给出 `cachedPages=8`、`renderedPages=2`

### Requirement: 页级并行不改变产物

渲染器 MAY 并行渲染多页。并行度 MUST 可配置，默认 1（串行）。无论并行度为何，产物文件名与页序、单页降级归属、进度总量 MUST 与串行一致。

#### Scenario: 并行与串行产物一致

- **WHEN** 同一份 5 页计划分别以 `pageConcurrency=1` 与 `3` 渲染
- **THEN** 两次的片段集合与每页帧数 MUST 相同，逐页 SHA-256 MUST 相同

#### Scenario: 并行下的单页降级

- **WHEN** 并行渲染时第 2 页失败
- **THEN** 只有第 2 页 MUST 被标记为降级，其余页不受影响
