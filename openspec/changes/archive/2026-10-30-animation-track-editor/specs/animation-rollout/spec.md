# animation-rollout Specification（M4 增量）

## ADDED Requirements

### Requirement: 能力判定收敛到单一纯函数

动画能力的开放判定 MUST 由一个无 I/O 的函数给出，输入为「租户 ID + 部署配置 + 租户策略」，输出为一份能力快照。写路径、读路径、导出选项与导出入口 MUST 使用同一判定，MUST NOT 各自读环境变量或各自解释策略。

#### Scenario: 三处入口答案一致

- **WHEN** 某租户被显式关闭
- **THEN** 导出选项 MUST 返回 `enabled=false` 且带 `reason`，写规格 MUST 返回 404，读规格 MUST 仍返回 200

#### Scenario: 部署闸关闭时租户策略无法开启

- **WHEN** 部署闸 `PPTS_ANIMATION_ENABLED=false`，某租户策略写着 `enabled:true`
- **THEN** 能力判定 MUST 为不可用，`source` MUST 为部署侧，`reason` MUST 为 `deploy_disabled`

### Requirement: 显式策略优先于灰度

租户策略的显式 `enabled` MUST 压过灰度百分比：显式 `true` 在灰度 0% 时仍可用；显式 `false` 在灰度 100% 时仍不可用。未设置时 MUST 依次回落到租户的 `rollout_percent`，再回落到部署级默认百分比。

策略字段 MUST 区分「未设置」与「显式 false」：未设置走灰度，显式 false 是封禁。

#### Scenario: 白名单在 0% 灰度期可用

- **WHEN** `PPTS_ANIMATION_ROLLOUT=0`，某租户策略为 `enabled:true`
- **THEN** 该租户的能力 MUST 为可用，`source` MUST 为 `tenant`

#### Scenario: 显式关闭压过 100% 灰度

- **WHEN** `PPTS_ANIMATION_ROLLOUT=100`，某租户策略为 `enabled:false`
- **THEN** 该租户 MUST 不可用，`reason` MUST 为 `tenant_disabled`

#### Scenario: 未设置时按桶

- **WHEN** 租户没有 `enabled`，部署默认百分比为 20
- **THEN** 桶号 < 20 的租户 MUST 可用（`source=rollout`），其余 MUST 不可用（`reason=rollout_miss`）

### Requirement: 灰度分桶稳定、均匀且不可重排

分桶 MUST 由 `SHA-256(salt + 分隔符 + 租户 ID)` 的前 8 字节取模 100 得出。同一租户 ID 与同一 salt MUST 永远得到同一个桶；salt MUST NOT 随灰度批次变化。

改变 salt 会把已开放的租户踢回未开放状态，与「灰度只单调扩大」的语义冲突，故 salt 必须作为固定部署配置，不得按批次轮换。

#### Scenario: 分桶稳定

- **WHEN** 同一租户 ID 连续计算 1000 次
- **THEN** 每次结果 MUST 相同，且落在 `[0, 100)`

#### Scenario: 分布不聚集

- **WHEN** 对 10000 个形如 `t_000001` 的租户 ID 分桶
- **THEN** 每个十分位（0–9、10–19、…、90–99）的占比 MUST 在 7%–13% 之间

### Requirement: 关闭开关后 0 副作用

租户被关闭或部署闸关闭时，新的导出 MUST 产出与「从未引入动画」完全一致的静态产物；能力关闭 MUST NOT 改变已有产物、已有规格、既有导出结果。已受理的渲染任务 MUST NOT 因关闭而失败或中止。

#### Scenario: 关闭后导出与静态版逐字节相同

- **WHEN** 同一份文档在动画关闭前后各导出一次
- **THEN** 两次产物 MUST 逐字节相同，且响应 MUST 带 `animationDropped` 说明原因

#### Scenario: 在途任务不受影响

- **WHEN** 一个渲染任务已被受理，此时租户被关闭
- **THEN** 该任务 MUST 照常完成，产物 MUST 照常交付

### Requirement: 能力响应可诊断

能力快照 MUST 报出 `enabled`、`trackEditor`、`preview`、`source`、`bucket` 与 `reason`（可用时省略）。`reason` MUST 区分部署未开（`deploy_disabled`）、租户封禁（`tenant_disabled`）、未进灰度（`rollout_miss`）、未配置默认关（`default_off`）与策略不可读（`policy_unreadable`）。

#### Scenario: 未进灰度的租户看到原因

- **WHEN** 租户桶号 87，部署默认百分比 20
- **THEN** 响应 MUST 为 `enabled=false`、`source=rollout`、`bucket=87`、`reason=rollout_miss`

### Requirement: 子能力不得越过总开关

`track_editor` 与 `preview` MUST 仅在总能力可用时为真。`track_editor` 未设置时 MUST 为 false；`preview` 未设置时 MUST 随总开关（默认 true）。

#### Scenario: 总开关关闭时子能力全灭

- **WHEN** 租户策略为 `enabled:false`、`track_editor:true`、`preview:true`
- **THEN** 三者 MUST 全为 false
