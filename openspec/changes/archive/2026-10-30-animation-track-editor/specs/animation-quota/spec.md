# animation-quota Specification（M4 增量）

## ADDED Requirements

### Requirement: 渲染分钟按账期计量

动画渲染 M 的计量种类 MUST 带账期（UTC 月）后缀，每个账期 MUST 是额度表里独立的一行。额度上限取自租户策略的 `animation.max_minutes`；未配置 MUST 视为不限量。

账期 MUST 按 UTC 计算，MUST NOT 依赖实例本地时区——否则同一租户在不同节点上会在不同瞬间重置额度。

#### Scenario: 跨月是新的额度行

- **WHEN** 同一租户在 2026-09 与 2026-10 各消耗 10 分钟
- **THEN** MUST 存在两行额度记录，各自累计 10 分钟，MUST NOT 累加成 20 分钟

#### Scenario: 未配置上限不限量

- **WHEN** 租户策略没有 `animation.max_minutes`
- **THEN** 导出 MUST NOT 因额度被拒绝，额度行 MUST 为不限量

### Requirement: 受理时预占，超限明确拒绝

导出请求带动画时，服务 MUST 在**创建任务之前**按时间轴总时长预占渲染分钟数。预占失败（超出上限）MUST 返回 429 且带 `code=animation_quota_exceeded`，并在响应体给出上限、已用、本次申请量与恢复时间；MUST NOT 创建任务后再失败，MUST NOT 静默排队。

预占 MUST 幂等：同一幂等键重放 MUST 复用既有预占，MUST NOT 重复计入。

#### Scenario: 超限不入队

- **WHEN** 租户上限 4 分钟，提交一段 5 分钟的动画导出
- **THEN** 响应 MUST 为 429、`code=animation_quota_exceeded`，且 MUST NOT 创建任务

#### Scenario: 重放不重复预占

- **WHEN** 同一幂等键的导出请求被提交两次
- **THEN** 额度 MUST 只被预占一次

### Requirement: 按实际渲染的页结算

渲染完成 MUST 以「实际渲了动画的页」的时长结算，MUST NOT 按整片时长结算：降级成静态段的页 MUST NOT 计费。整体退回静态路径时 MUST 结算 0 分钟（释放预占），MUST NOT 保持预占不放。

结算 MUST 使用任务创建时间所在账期，MUST NOT 使用结算时刻的账期——跨月运行的任务结算到受理账期，否则找不到预占。

#### Scenario: 降级页不计费

- **WHEN** 一段 60 秒的导出有 3 页，其中第 2 页（20 秒）因渲染失败降级为静态段
- **THEN** 结算量 MUST 为 40 秒（10 + 30）对应的分钟数

#### Scenario: 整体降级不收费

- **WHEN** 渲染服务不可达，整片退回静态路径
- **THEN** 结算量 MUST 为 0，预占 MUST 被释放

### Requirement: 失败与取消释放预占

任务进入失败终态或取消时，服务 MUST 释放该任务的动画额度预占。可重试的中间失败 MUST NOT 释放——重试期间释放会让重试成功后的结算找不到预占。

#### Scenario: 失败终态释放

- **WHEN** 带动画的导出任务失败且不再重试
- **THEN** 该任务的预占 MUST 被释放，额度 MUST 回到可用

#### Scenario: 重试期间保留

- **WHEN** 任务第一次尝试失败并进入重试等待
- **THEN** 预占 MUST 仍然占用，MUST NOT 释放

### Requirement: 并发预占不超卖

额度预占 MUST 在存储层以单行条件更新保证原子性：并发请求下 `预占 + 已用` MUST NOT 超过上限。

#### Scenario: 并发不超卖

- **WHEN** 上限 10 分钟、每单 4 分钟，30 个请求并发提交
- **THEN** MUST 只有 2 单成功，其余 MUST 返回 429，且预占总量 MUST 等于 8 分钟

### Requirement: 静态导出不占动画额度

不带动画的导出 MUST NOT 预占或消耗动画渲染额度。

#### Scenario: 静态导出不受额度约束

- **WHEN** 动画额度已用尽，用户导出静态 MP4
- **THEN** 导出 MUST 成功受理
