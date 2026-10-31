# Spec Delta

## MODIFIED Requirements

### Requirement: 时间在编译期解析为页内绝对毫秒

`withSegment:<id>` MUST 被解析为该分段相对本页起点的毫秒偏移；`atTime:<ms>` MUST 直接取值；`onEnter`
MUST 为 0。解析后 MUST 满足 `0 ≤ startMs` 且（`continues = false` 时）`startMs + durationMs ≤
pageDurationMs`。

**跨页延续（新增）**：当 clip `continues = true` 且 `startMs + durationMs > pageDurationMs` 时，编译器
MUST NOT 截断该 clip，而 MUST 按「跨页延续在编译期拆分」要求将其拆分到下一页。无法解析的分段 MUST 记录
`segment_unresolved` 诊断并把该 clip 的起点退化为 0（与既有规则一致）。

#### Scenario: 分段锚定落在页中

- **WHEN** 某页从全局 12000ms 开始，第 3 段从全局 15500ms 开始，clip 触发为 `withSegment:seg-3`
- **THEN** IR 中该 clip 的 `startMs` 为 3500

#### Scenario: 锚定失效

- **WHEN** `withSegment:seg-9` 在当前讲稿修订中不存在
- **THEN** 该 clip 的 `startMs` 为 0，IR 带 `segment_unresolved` 诊断，页面仍可渲染

## ADDED Requirements

### Requirement: 跨页延续在编译期拆分

当 clip `continues = true` 且 `startMs + durationMs > pageDurationMs` 时，编译器 MUST 把该 clip 拆为两段
Per-page Motion：本页段（`isContinuation = false`）与次页段（`isContinuation = true`，两段 `elementId`
相同，来自 `Track.element_id`）。播放器/渲染器评估次页段 MUST 用 `clipTime = localMs + continuationOffsetMs`
保证页缝处视觉连续；`continues = false` 的 clip 保持既有截断行为。

#### Scenario: 延续 clip 在页边界拆分为两段

- **WHEN** 一个 `continues = true` 的 clip 的 `startMs + durationMs` 超过本页时长，且次页存在同 `elementId` 层
- **THEN** 编译器 MUST 产出本页段（isContinuation=false）与次页段（isContinuation=true，
  continuationOffsetMs = 页时长 - startMs），两段 `elementId` 相同

#### Scenario: 次页无承接元素时退化为截断

- **WHEN** 一个 `continues = true` 的 clip 溢出页尾，但次页无同 `elementId` 层
- **THEN** 编译器 MUST 记录 `continuation_target_missing` 诊断，该 clip 退化为本页截断，MUST NOT 阻断整页

#### Scenario: 非延续 clip 仍截断

- **WHEN** 一个 `continues = false` 的 clip 溢出页尾
- **THEN** 编译器 MUST 记录 `clip_truncated` 并按既有规则截断/丢弃，行为不变
