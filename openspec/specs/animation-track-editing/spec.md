# animation-track-editing Specification

## Purpose
定义元素级轨道编辑的语义边界：受哪个能力位控制、拖动后时间以什么为准、边界如何夹紧、撤销如何不破坏并发保存。目标是让「拖了就该变」成立——入场片段的起点由触发器推导，只改 `startMs` 的结果是保存成功、成片不变、且不报错，这类缺陷不会在任何门禁里自己暴露，只能靠规格写死。

## Requirements

### Requirement: 元素级轨道编辑受能力开关控制

轨道时间轴 MUST 仅在租户能力 `trackEditor` 为真时可用。未开通时 MUST 只显示说明文案，MUST NOT 渲染可交互但保存必然失败的控件。

#### Scenario: 未开通时无可交互控件

- **WHEN** `capabilities.trackEditor` 为 false
- **THEN** 面板 MUST 显示未开通说明，MUST NOT 出现可拖动的色块

#### Scenario: 页时长未知时不允许编排

- **WHEN** 该页还没有配音（页时长为 0）
- **THEN** MUST 提示先完成配音，MUST NOT 允许拖动

### Requirement: 拖动派生起点的片段必须显式化时间

入场片段的起点由触发器决定（`onEnter` 为页首，`withSegment:<id>` 为分段开口）。当用户拖动或微调这类片段时，实现 MUST 同时把触发器改写为 `atTime:<startMs>`。

只改 `startMs` 而不改触发器，会让界面上的改动在成片里完全不生效，且没有任何报错——这是本功能最容易出现的"假成功"。

#### Scenario: 拖动入场片段

- **WHEN** 用户把一条 `onEnter` 片段从 0ms 拖到 1200ms
- **THEN** 该片段 MUST 为 `startMs=1200` 且 `trigger="atTime:1200"`

#### Scenario: 只改时长不改触发器

- **WHEN** 用户拖动片段右边缘把时长从 400ms 改为 700ms
- **THEN** `durationMs` MUST 为 700，`trigger` MUST 保持不变

### Requirement: 编辑范围与服务端校验一致

拖动与键盘微调 MUST 在客户端夹紧到服务端允许的范围：起点 ≥ 0、时长 ∈ [120, 3000]ms、起点 + 时长 ≤ 页时长（页时长未知时跳过上界）。MUST NOT 允许用户拖出一个必然被服务端拒绝的规格。

改变时长导致页尾放不下时 MUST 截断时长并保持起点不变；MUST NOT 反向移动起点。

#### Scenario: 拖到页尾

- **WHEN** 一条 400ms 的片段被拖到页尾，用户把时长拖到 3000ms 而页尾只剩 500ms
- **THEN** 时长 MUST 截断为 500ms，起点 MUST 保持不变

### Requirement: 撤销重做覆盖轨道编辑且至少 20 步

轨道拖动与键盘微调 MUST 支持撤销与重做，栈深 MUST ≥ 20 步。一次连续拖拽 MUST 只记一步撤销，MUST NOT 按指针移动逐次记录。

撤销 MUST NOT 改变当前并发版本号：还原快照后再次保存 MUST 使用当前 `revision` 作为 `expectedRevision`。

#### Scenario: 一次拖拽一步撤销

- **WHEN** 用户按住色块拖动 200 像素后松开
- **THEN** 撤销一次 MUST 回到拖动前的起点

#### Scenario: 撤销后保存不冲突

- **WHEN** 用户保存成功后撤销一次编辑，再保存
- **THEN** 保存 MUST 使用保存后的版本号，MUST NOT 因版本号过期失败

### Requirement: 键盘可微调

聚焦到片段后，方向键 MUST 将起点移动 10ms，Shift + 方向键 MUST 移动 50ms。

#### Scenario: 键盘微调

- **WHEN** 片段获得焦点后按右方向键
- **THEN** 起点 MUST 增加 10ms；按住 Shift 时 MUST 增加 50ms
