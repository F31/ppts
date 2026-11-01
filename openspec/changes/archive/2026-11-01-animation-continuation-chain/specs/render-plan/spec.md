# Spec Delta

## MODIFIED Requirements

### Requirement: 跨页延续在编译期拆分

当 clip `continues = true` 且 `startMs + durationMs > pageDurationMs` 时，编译器 MUST 把该 clip 拆为
**覆盖整个 clip 时长**的若干 Per-page Motion：本页段（`isContinuation = false`）与后续各页的延续段
（`isContinuation = true`）。所有段的 `elementId` MUST 相同（来自 `Track.element_id`），且 Props MUST
与源段完全一致（MUST NOT 预拆分）。拆分范围 MUST 足以覆盖 `startMs + durationMs`；MUST NOT 只在次页
注入一段后停止，把余量静默交给片长裁掉。

每一跳的偏移量 MUST 满足「上一段在本页尾处的 clip 时间」：

- 首跳：`continuationOffsetMs = 页时长 - startMs`；
- 第 k 跳（k > 1）：`continuationOffsetMs = 上一跳 continuationOffsetMs + 上一页页长`。

每段 MUST 满足 `startMs = -continuationOffsetMs`。播放器/渲染器评估任一延续段 MUST 用
`clipTime = localMs + continuationOffsetMs`，使链上每一处页缝两侧的进度与原 clip 逐位一致。

链的终止与降级：

- 某页缺少同 `elementId` 层时，编译器 MUST 记录 `continuation_target_missing` 并停止后续注入；
  **已注入的段 MUST 保留**（元素在中断页之前仍连续），且 MUST NOT 阻断整页。
- 到达末页仍有余量时，编译器 MUST 记录 `continuation_target_missing`；余量由片长裁掉，MUST NOT 阻断。
- `continues = false` 的 clip 保持既有截断行为。

#### Scenario: 延续 clip 在页边界拆分为两段

- **WHEN** 一个 `continues = true` 的 clip 的 `startMs + durationMs` 超过本页时长，且次页存在同 `elementId` 层
- **THEN** 编译器 MUST 产出本页段（isContinuation=false）与次页段（isContinuation=true，
  continuationOffsetMs = 页时长 - startMs），两段 `elementId` 相同

#### Scenario: 跨三页的延续按页逐跳拆分

- **WHEN** 一个 `continues = true` 的 clip 的时长超过「本页剩余 + 次页整页」，且第 3 页存在同 `elementId` 层
- **THEN** 编译器 MUST 在第 3 页再产出第 2 跳延续段，其 `continuationOffsetMs =
  首跳 offset + 次页页长`、`startMs = -continuationOffsetMs`、`durationMs` 仍为原长、
  Props 与源段一致

#### Scenario: 链中某页缺承接元素时停止注入

- **WHEN** 链式延续进行到某一页，该页无同 `elementId` 层
- **THEN** 编译器 MUST 记录 `continuation_target_missing` 并停止后续注入，已注入的段 MUST 保留，
  MUST NOT 阻断整页

#### Scenario: 次页无承接元素时退化为截断

- **WHEN** 一个 `continues = true` 的 clip 溢出页尾，但次页无同 `elementId` 层
- **THEN** 编译器 MUST 记录 `continuation_target_missing` 诊断，该 clip 退化为本页截断，MUST NOT 阻断整页

#### Scenario: 末页仍有余量

- **WHEN** 链式延续到达最后一页，该页结束时 clip 仍未演完
- **THEN** 编译器 MUST 记录 `continuation_target_missing`，余量被片长裁掉，MUST NOT 报错或阻断

#### Scenario: 非延续 clip 仍截断

- **WHEN** 一个 `continues = false` 的 clip 溢出页尾
- **THEN** 编译器 MUST 记录 `clip_truncated` 并按既有规则截断/丢弃，行为不变
