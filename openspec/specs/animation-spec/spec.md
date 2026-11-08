# animation-spec Specification

## Purpose
定义单页动画规格（`AnimationSpec`）的数据结构、默认生成规则、服务端校验规则与版本语义。它是动画通道的唯一事实源：编辑器按它渲染表单，编排层按它编译渲染计划，渲染器永远不直接读它。

## Requirements

### Requirement: 规格按隔离维度唯一

系统 SHALL 以 `(tenant_id, project_id, source_revision_no, slide_id, language)` 作为一份动画规格的唯一标识，同一维度下 MUST 最多存在一条记录。`source_revision_no = 0` MUST 按 legacy 语义处理，与讲稿的回退可见规则一致。

#### Scenario: 同维度重复写入

- **WHEN** 对已存在的 `(tenant, project, sourceRevisionNo, slideId, language)` 再次插入规格
- **THEN** 系统返回既有记录并递增其 `revision`，MUST NOT 产生第二条记录或唯一键冲突错误

#### Scenario: 不同源版本互不干扰

- **WHEN** 同一页在源版本 2 与源版本 3 下各有一份规格
- **THEN** 读取源版本 3 时 MUST 只返回源版本 3 的规格，源版本 2 的规格保持原样

### Requirement: 规格结构为三层轨道

一份规格 SHALL 由页级属性（`preset_id`、`intensity`）与 `tracks[]` 组成；每个 `Track` MUST 通过
`element_id` 指向该页某个 `project.Shape.ID`，`element_id` 为空字符串时 MUST 表示页面级轨道。
`Track.clips[]` MUST 按页内相对时间表达动效，单个 clip MUST 包含 `type`、`trigger`、`start_ms`、
`duration_ms`、`easing`。

**跨页延续（新增）**：`Clip` MAY 携带 `continues: true`，表示该 clip 允许跨页延续到下一页。当
`continues = true` 时，`start_ms + duration_ms` MAY 超过该页时长（见「时间边界与页时长预算」的放宽）；
跨页身份复用 `Track.element_id`（相邻页同 shape 共享 shape id 即为同一延续实体）。`continues` 缺省
为 false，此时行为 MUST 与引入本需求前完全一致（页内相对、超界被截断）。

#### Scenario: 合法规格被接受

- **WHEN** 提交包含 1 条标题轨道（`onEnter` 滑入）与 1 条要点轨道（`withSegment` 淡入）的规格
- **THEN** 校验通过，规格按提交内容原样持久化，`revision` 递增

#### Scenario: 缺失必填字段

- **WHEN** 某个 clip 缺少 `type` 或 `duration_ms`
- **THEN** 系统返回 400，并指出具体 clip 的 id 与缺失字段

#### Scenario: 普通 clip 仍页内相对

- **WHEN** 一个 `continues = false`（缺省）的 clip 的 `start_ms + duration_ms` 超过页时长
- **THEN** 系统 MUST 按既有规则截断/拒绝（行为不变），MUST NOT 跨页延续

#### Scenario: 延续 clip 复用 element_id 跨页链接

- **WHEN** 一个 `continues = true` 的 clip 的 `Track.element_id` 在下一页也存在同名 shape
- **THEN** 编译 MUST 把该 clip 的动效延续到下一页同一元素上（见 render-plan 的拆分要求）

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

`start_ms` MUST ≥ 0；`start_ms + duration_ms` MUST ≤ 该页时长（由 `Timeline` 的
`(EndUS-StartUS)/1000` 得出）加 1 帧容差。整页所有 clip 的归一化覆盖时长 MUST ≤ 页时长。越界 MUST 被
拒绝，渲染阶段 MUST NOT 负责裁剪。

**跨页延续放宽（新增）**：当 clip `continues = true` 时，`start_ms + duration_ms` MAY 超过页时长，其
超出部分 MUST 由编译阶段在页边界拆分到下一页（见 render-plan「跨页延续在编译期拆分」），而 MUST NOT 在
规格保存阶段被截断或拒绝。非 `continues` 的 clip 仍 MUST 遵守「≤ 页时长」约束。

当页时长未知（尚无配音与时间轴，`durationMS = 0`）时，系统 SHALL 只校验 `start_ms ≥ 0` 与 clip 时长
区间，MUST NOT 因时长未知拒绝保存。此时 MUST 由编译/渲染前用真实页时长重新校验兜底，且该重校验 MUST NOT
被跳过；重校验对 `continues` 的 clip 改为「拆分而非截断」。

#### Scenario: clip 超出页尾

- **WHEN** 页时长 6400ms，某 clip 的 `start_ms = 6000`、`duration_ms = 900`
- **THEN** 系统返回 400，并在错误信息中给出该页时长上限

#### Scenario: 合法贴合页尾

- **WHEN** 页时长 6400ms，clip 为 `start_ms = 5800`、`duration_ms = 600`
- **THEN** 校验通过

#### Scenario: 尚无配音时保存

- **WHEN** 该页还没有配音，页时长未知，用户提交 `start_ms = 12000` 的合法结构规格
- **THEN** 保存成功，响应 MUST 标记该规格需要渲染前重新校验时长

#### Scenario: 延续 clip 超页时长在保存期不被拒绝

- **WHEN** 一个 `continues = true` 的 clip 在保存时 `start_ms + duration_ms` 超过当前页时长
- **THEN** 保存 MUST 成功（标记该规格需渲染前重校验），MUST NOT 静默截断为页时长，MUST NOT 报错

#### Scenario: 非延续 clip 超页时长仍被拒绝

- **WHEN** 一个 `continues = false` 的 clip 在保存时 `start_ms + duration_ms` 超过页时长且页时长已知
- **THEN** 系统 MUST 返回明确错误，MUST NOT 静默截断为合法值

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

### Requirement: 机器生成的绑定必须标注来源

当 `Clip.trigger` 由系统预填而非用户手动设置时，规格 MUST 记录该绑定的来源，取值 MUST 属于白名单：
`literal`（字面相似）/ `count`（数量词→编号组）/ `ordinal`（第N条→第N块）/ `spatial`（方位指代）/
`sibling`（同块带出）/ `title-prior`（标题先验）/ `structural`（结构推断）/ `manual`。

来源字段 MUST 以 `omitempty` 序列化：不含来源的规格 MUST 与引入本字段前逐字节一致，`planHash` MUST 不变。
来源 MUST NOT 影响求值——编译器、播放器、渲染器 MUST NOT 因来源不同而改变输出。

用户修改一条预填绑定的 `trigger` 后，该绑定的来源 MUST 变为 `manual`，MUST NOT 保留原规则名：
过期的来源比没有来源更糟，它会让「按来源筛选」筛出一批其实已被人工改过的条目。

#### Scenario: 既有规格字节不变

- **WHEN** 加载并重新保存一份不含来源标注的既有规格
- **THEN** 序列化结果 MUST 与引入本字段前逐字节一致，`planHash` MUST 相同

#### Scenario: 未知来源被拒绝

- **WHEN** 写入的 `triggerSource` 不在白名单内
- **THEN** 保存 MUST 失败并返回允许的取值列表，MUST NOT 静默丢弃该字段

#### Scenario: 用户手改后来源失效

- **WHEN** 用户修改了一条来源为 `sibling` 的预填绑定的触发方式
- **THEN** 该绑定的来源 MUST 变为 `manual`，按来源 `sibling` 筛选时 MUST NOT 再包含它

### Requirement: 批量套用动效必须全成或全不成

系统 SHALL 支持以某一页已保存的动效风格（`presetId` 与 `intensity`）为模板，一次请求套用到多选的若干页。
该操作 MUST 采用「预检全部目标页 → 单事务提交」两段式：任何一页不满足条件时
MUST 返回带逐页原因的 `422 batch_ineligible` 且 MUST NOT 写入任何一页；
事务中任一页发生并发冲突时 MUST 整体回滚，MUST NOT 留下部分页已套用的中间态。
系统 MUST NOT 把「尽力而为、单页失败降级为 skipped」的 `apply-all` 语义用于本操作。

源页与目标页 MUST 都属于当前 `sourceRevisionNo` 的 deck；源页 MUST 已有保存的规格
且 MUST 带非空 `presetId`（自定义规格没有可套用的风格，MUST 整单拒绝，MUST NOT 静默退回默认预设）；
`locked` 状态的页 MUST 永不被越过（即使请求显式要求覆盖）。
系统 MUST 把「套用」实现为风格套用：每个目标页 MUST 用自身的元素与讲稿分段重新生成轨道，
并与「单页打开选同一预设」共用同一条生成链。系统 MUST NOT 把源页的 `tracks`
（其 `elementId` 与 `withSegment` 触发器都是页内标识）或 `Shots`（页内 EMU 裁切框）复制到目标页，
也 MUST NOT 通过放宽 V2 锚点校验来达成跨页复制。

#### Scenario: 一页不合格导致整单零写入

- **WHEN** 用户以第 3 页为模板勾选第 5、8、12 页，其中第 8 页处于 `locked` 状态
- **THEN** 系统返回 `422 batch_ineligible`，`results` 列出第 8 页原因为 `locked`
- **AND** 第 5、12 页的规格 MUST 与请求前逐字节一致，MUST NOT 产生新 revision

#### Scenario: 全部合格才提交

- **WHEN** 所有目标页都在 deck 内、未被锁定、页时长已知且按本页元素重新生成的规格通过 V1–V12
- **THEN** 系统 MUST 在一个事务内写入全部目标页，响应 `applied` 等于目标页数，
      且 `results` 中 `status=applied` 的条数与 `previous` 快照条数 MUST 都等于 `applied`

#### Scenario: 事务中途冲突整体回滚

- **WHEN** 预检通过后、提交时某一页的 `revision` 已被其它请求改过
- **THEN** 系统 MUST 返回 `409 batch_conflict` 并说明「整批已回滚」，
      MUST NOT 保留同批其它页的写入

#### Scenario: 跨页不复用源页轨道

- **WHEN** 源页轨道锚定 `shape-1`，而目标页只有 `shape-1-slide-2` 这类不重合的元素 ID
- **THEN** 批量套用 MUST 成功，目标页轨道 MUST 只锚定本页元素 ID，MUST NOT 出现源页的 `shape-1`
- **AND** 目标页写入的规格 MUST NOT 带源页的 `Shots`

### Requirement: 批量套用必须可用一次撤销

系统 SHALL 在批量套用成功响应中返回 `previous[]` 快照，快照 MUST 由服务端在写入前读取，
MUST NOT 由客户端自行推断「之前是什么」。系统 SHALL 提供 `apply-batch/restore` 端点，
按快照在单事务内恢复：套用前没有规格的页 MUST 删除该行（`Rev=nil`），
MUST NOT 写成一份空草稿。恢复内容 MUST 重新通过 V1–V12 校验，
快照页 MUST 属于当前 deck，任一快照不合法时 MUST 整单拒绝且零写入。

#### Scenario: 撤销还原有规格的页

- **WHEN** 用户撤销一次批量套用，某目标页套用前已有规格
- **THEN** 该页的 `presetId` / `intensity` / `tracks` MUST 恢复为快照内容，
      且 `status` 与其它未纳入快照的字段 MUST 遵循既有 `Put` 语义

#### Scenario: 撤销删除原本无规格的页

- **WHEN** 某目标页在套用前没有规格（快照 `existed=false`）
- **THEN** 撤销后该页 MUST 回到「从没配过」状态（记录不存在），
      MUST NOT 留下 `presetId` 为空的草稿行

#### Scenario: 脏快照不写入

- **WHEN** 恢复请求里的快照含重复页 ID、不属于当前 deck 的页，或轨道内容无法通过校验
- **THEN** 系统 MUST 返回 `422 restore_ineligible` / `409 batch_conflict`，
      MUST NOT 改动任何一页

### Requirement: 前端不得把未经核验的批量结果显示为成功

编辑器 SHALL 在页列表提供多选态与「套用当前页动效」入口，并 MUST 在显示成功前
复核响应自洽（`applied` 汇总 = 逐页 `applied` 条数 = 快照条数，且三者非零）。
撤销按钮 MUST 仅在通过快照自检后可用。预检被拒时编辑器 MUST 展示逐页原因汇总，
MUST NOT 只报一句笼统的操作失败。错误兜底文案 MUST 使用已在中英文案表中登记的键。

#### Scenario: 汇总与逐页结果不一致

- **WHEN** 后端返回 `applied=5` 但 `results` 里只有 3 条 `applied`，或只给了 3 份快照
- **THEN** 编辑器 MUST NOT 显示「已套用」，MUST 显示无法核验的提示，且撤销按钮 MUST 保持禁用

#### Scenario: 预检被拒展示可行动原因

- **WHEN** 后端返回 `422 batch_ineligible`，逐页原因为 1 条 `duration_unknown` 与 2 条 `locked`
- **THEN** 编辑器 MUST 明确说明「本次没有改动任何一页」并列出这两类原因及条数

#### Scenario: 多选态不改变单页编辑器行为

- **WHEN** 用户处于多选态并点击左侧页轨的某一页
- **THEN** 该页 MUST 只切换勾选状态，MUST NOT 切换当前编辑页；模板页 MUST 保持为进入多选时的当前页
