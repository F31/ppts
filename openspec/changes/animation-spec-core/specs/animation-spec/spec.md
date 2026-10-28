# Spec Delta

## Purpose

定义单页动画规格（`AnimationSpec`）的数据结构、默认生成规则、服务端校验规则与版本语义。它是动画通道的唯一事实源：编辑器按它渲染表单，编排层按它编译渲染计划，渲染器永远不直接读它。

## ADDED Requirements

### Requirement: 规格按隔离维度唯一

系统 SHALL 以 `(tenant_id, project_id, source_revision_no, slide_id, language)` 作为一份动画规格的唯一标识，同一维度下 MUST 最多存在一条记录。`source_revision_no = 0` MUST 按 legacy 语义处理，与讲稿的回退可见规则一致。

#### Scenario: 同维度重复写入

- **WHEN** 对已存在的 `(tenant, project, sourceRevisionNo, slideId, language)` 再次插入规格
- **THEN** 系统返回既有记录并递增其 `revision`，MUST NOT 产生第二条记录或唯一键冲突错误

#### Scenario: 不同源版本互不干扰

- **WHEN** 同一页在源版本 2 与源版本 3 下各有一份规格
- **THEN** 读取源版本 3 时 MUST 只返回源版本 3 的规格，源版本 2 的规格保持原样

### Requirement: 规格结构为三层轨道

一份规格 SHALL 由页级属性（`preset_id`、`intensity`）与 `tracks[]` 组成；每个 `Track` MUST 通过 `element_id` 指向该页某个 `project.Shape.ID`，`element_id` 为空字符串时 MUST 表示页面级轨道。`Track.clips[]` MUST 按页内相对时间表达动效，单个 clip MUST 包含 `type`、`trigger`、`start_ms`、`duration_ms`、`easing`。

#### Scenario: 合法规格被接受

- **WHEN** 提交包含 1 条标题轨道（`onEnter` 滑入）与 1 条要点轨道（`withSegment` 淡入）的规格
- **THEN** 校验通过，规格按提交内容原样持久化，`revision` 递增

#### Scenario: 缺失必填字段

- **WHEN** 某个 clip 缺少 `type` 或 `duration_ms`
- **THEN** 系统返回 400，并指出具体 clip 的 id 与缺失字段

### Requirement: 动效类型受白名单约束

`Clip.type` 与页面级 `preset_id` MUST 来自动效 catalog；catalog 之外的取值 MUST 被拒绝。catalog SHALL 为每个动效声明适用元素类型与参数取值范围，系统 MUST 依据声明校验参数，而不是依据硬编码分支。

#### Scenario: 未登记的动效被拒绝

- **WHEN** 提交 `type: "explode"`
- **THEN** 系统返回 400，错误信息 MUST 列出 catalog 中可用的动效 id

#### Scenario: 参数越界被钳制拒绝

- **WHEN** `slideIn` 的 `params.offset` 提交为 `500`（catalog 上限 120）
- **THEN** 系统返回 400 并给出允许区间，MUST NOT 静默截断为合法值

#### Scenario: 动效不适用该元素

- **WHEN** 对 `chart` 轨道提交仅适用于 `title|bullet` 的动效
- **THEN** 系统返回 400，并说明该动效适用的元素类型

### Requirement: 元素锚点必须存在于当前页

`Track.element_id` 非空时 MUST 命中该页 `Document.Shapes`（含 `Children` 递归）；无法命中的锚点 MUST 被拒绝，且错误信息 SHALL 列出该页可用于挂载的元素 id。

#### Scenario: 引用已删除的元素

- **WHEN** 提交的 `element_id` 指向当前源版本中已不存在的形状
- **THEN** 系统返回 400，并在返回体列出可用元素 id

### Requirement: 时间边界与页时长预算

`start_ms` MUST ≥ 0；`start_ms + duration_ms` MUST ≤ 该页时长（由 `Timeline` 的 `(EndUS-StartUS)/1000` 得出）加 1 帧容差。整页所有 clip 的归一化覆盖时长 MUST ≤ 页时长。越界 MUST 被拒绝，渲染阶段 MUST NOT 负责裁剪。

#### Scenario: clip 超出页尾

- **WHEN** 页时长 6400ms，某 clip 的 `start_ms = 6000`、`duration_ms = 900`
- **THEN** 系统返回 400，并在错误信息中给出该页时长上限

#### Scenario: 合法贴合页尾

- **WHEN** 页时长 6400ms，clip 为 `start_ms = 5800`、`duration_ms = 600`
- **THEN** 校验通过

### Requirement: 同元素动效不得静默重叠

同一元素上，入场类动效与退场类动效各 MUST 最多一条，强调类动效可多条但 MUST NOT 与同类强调动效时间重叠。冲突 MUST 被拒绝并指出冲突的 clip 对。

#### Scenario: 两条入场动效重叠

- **WHEN** 同一元素存在两条 `fadeIn`，时间区间相交
- **THEN** 系统返回 400，错误信息包含两条 clip 的 id

### Requirement: 触发锚定支持相对讲稿分段

`Clip.trigger` MUST 支持 `onEnter`、`withSegment:<segmentId>`、`atTime:<ms>` 三种形式。`withSegment` 引用的 segment MUST 存在于当前 `source_revision_no` 对应的讲稿修订；引用失效时系统 MUST 返回 409 并提示讲稿已变更，MUST NOT 静默改写为 `onEnter`。

#### Scenario: 锚定到已删除的分段

- **WHEN** 规格引用 `withSegment:seg-9`，而当前讲稿修订中不存在 `seg-9`
- **THEN** 系统返回 409，响应体指出失效的 segmentId

#### Scenario: 锚定有效时保留相对语义

- **WHEN** 规格以 `withSegment:seg-2` 触发
- **THEN** 规格持久化时 MUST 保留原始相对引用，MUST NOT 在写路径把该引用固化为绝对毫秒

### Requirement: 乐观并发写入

写路径 MUST 接受 `expected_revision`；当它与库中 `revision` 不一致时，系统 MUST 返回 409 `ErrConflict` 并附带最新版本内容，MUST NOT 覆盖他方修改。校验与写入 MUST 在同一事务语义内完成。

#### Scenario: 并发编辑同一页

- **WHEN** A 基于 `revision = 7` 提交成功，B 仍以 `expected_revision = 7` 提交
- **THEN** B 收到 409，响应体携带 A 写入的 `revision = 8` 的完整规格

#### Scenario: 校验失败不写入

- **WHEN** 提交的规格违反任一校验规则
- **THEN** 系统 MUST 保持库中记录不变，`revision` 不递增

### Requirement: 状态机与锁定

规格状态 MUST 遵循 `draft → approved → locked`，`locked` MAY 回退到 `approved`。处于 `locked` 的规格 MUST 拒绝任何写入，返回 409 `ErrLocked`，且不得因校验顺序不同而返回 400。

#### Scenario: 锁定后写入

- **WHEN** 规格状态为 `locked`，用户提交任意合法修改
- **THEN** 系统返回 409 `ErrLocked`，并展示锁定人与锁定时间

### Requirement: 默认规格可零配置生成

系统 SHALL 能从页元素与预设 id 生成一份通过全部校验的默认规格，用于未配置过动画的页面。生成规则 MUST 是确定性的：相同输入 MUST 产出字节级相同的规格（除时间戳与 id 外）。默认生成 MUST NOT 要求任何渲染器信息。

#### Scenario: 首次打开页面

- **WHEN** 某页尚无规格，客户端以 `preset_id = business-calm` 请求默认规格
- **THEN** 系统返回一份含标题与要点轨道的规格，`status = draft`、`intensity = 1.0`，且直接通过 V1–V12 校验

#### Scenario: 元素缺少几何信息

- **WHEN** 页内某形状的 `bounds` 为空
- **THEN** 默认生成 MUST 跳过该元素并仍产出合法规格，MUST NOT 阻塞整页

### Requirement: 规格规模上限

单页规格 MUST 满足：轨道数 ≤ 30、clip 总数 ≤ 40、序列化后字节 ≤ 256KB。超限 MUST 返回明确错误码（400 或 413），并给出当前计数。

#### Scenario: 超出 clip 上限

- **WHEN** 单页提交 41 条 clip
- **THEN** 系统返回 400，错误信息含 `41 / 40` 形式的使用量与上限

### Requirement: 租户隔离不可绕过

规格读写 MUST 在租户上下文中执行；缺失租户上下文时 MUST 拒绝访问。数据库层 SHALL 启用行级隔离策略，跨租户读取 MUST 返回空结果而非他人数据。

#### Scenario: 跨租户读取

- **WHEN** 租户 A 的请求携带租户 B 的 `project_id`
- **THEN** 系统返回 404 或空结果，MUST NOT 返回 B 的规格内容
