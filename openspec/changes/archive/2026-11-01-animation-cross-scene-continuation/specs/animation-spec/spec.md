# Spec Delta

## MODIFIED Requirements

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
