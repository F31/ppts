# render-plan Specification（M5 增量）

## ADDED Requirements

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
