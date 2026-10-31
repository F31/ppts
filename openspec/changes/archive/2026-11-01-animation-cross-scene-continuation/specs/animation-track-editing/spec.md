# Spec Delta

## ADDED Requirements

### Requirement: 跨页延续可在编辑器表达

编辑器 MUST 允许把一条 `clip` 标记为 `continues = true`，并在该 clip 的 `start_ms + duration_ms` 超过
当前页时长时，**不**把它钳回页内（`clampClip` / `moveClipTo` / `resizeClipTo` 在 `continues` 时 MUST
放行溢出），而是标记为「延续到下一页」。编辑器 MUST 在 UI 上提示该 clip 将跨页延续，并要求作者在下一页
放置同 `element_id` 的元素以承接延续。

编辑器 MUST NOT 对非 `continues` 的 clip 改变既有钳制行为——`continues` 缺省为 false 时编辑算术仍以单页
`pageDurationMs` 为上界。

#### Scenario: 标记 continues 后可拖出页尾

- **WHEN** 用户在编辑器把某 clip 的 `continues` 设为 true 并把它拖到超过本页时长
- **THEN** 编辑器 MUST 接受该超出（不钳回），并提示「将延续到下一页」

#### Scenario: 非延续 clip 仍被钳制

- **WHEN** 用户把一条 `continues = false` 的 clip 拖到超过本页时长
- **THEN** 编辑器 MUST 保持既有钳制（maxStart = pageDurationMs - durationMs），行为不变
