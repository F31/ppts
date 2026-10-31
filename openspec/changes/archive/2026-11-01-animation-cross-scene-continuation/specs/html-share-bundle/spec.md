# Spec Delta

## ADDED Requirements

### Requirement: 清单表达跨页延续的层链接

打包产物（清单 `SlideLayer.Clips` / Motion）MUST 为延续段携带 `elementId`、`isContinuation` 与
`continuationOffsetMs` 三个字段，与 `render-plan` 的编译拆分输出一致；`elementId` MUST 等于编译期
透传的 `Track.element_id`，用于播放器换页时把次页层与本页同元素层链接为同一视觉实体。

#### Scenario: 延续段携带链接字段且不重放入场

- **WHEN** 清单中某 `SlideLayer.Clips` 的 `isContinuation = true` 且带 `elementId` / `continuationOffsetMs`
- **THEN** 播放器 MUST 跳过该层入场动效，并按 `clipTime = localMs + offset` 评估，页缝处视觉连续

#### Scenario: 旧播放器忽略新字段不崩

- **WHEN** 旧播放器（不识别 `isContinuation` / `continuationOffsetMs`）加载含延续段的清单
- **THEN** 次页段 MUST 退化为普通层（仅失去续播、可能缝处轻微回跳），MUST NOT 崩溃或露白

#### Scenario: 延续段 offset 为负被拒绝

- **WHEN** 清单中某延续段的 `continuationOffsetMs < 0`
- **THEN** 打包 MUST 记录诊断并拒绝该清单
