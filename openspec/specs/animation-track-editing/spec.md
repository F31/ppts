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

### Requirement: 讲稿绑定预填策略

系统 MAY 在用户尚未绑定时提供预填，但 MUST 遵守：

1. 确定性：同一 `(page, segments)` MUST 得到同一结果，MUST NOT 依赖随机数、当前时间、网络调用或
   map 遍历顺序。结果抖动会让用户不再信任预填，而信任一旦丢掉，预填等于没有。
2. 人工权威：预填 MUST NOT 覆盖用户已确认或已修改的绑定；用户清空绑定后 MUST NOT 被再次自动填回。
3. 可解释：每条预填 MUST 带来源标注（见 `animation-spec`）与连续置信度。
4. 分档：预填 MUST 先按版面结构信号分档；无结构信号的页 MUST 标记为低置信，MUST NOT 与结构化页
   混同呈现——两者的预期准确率相差一个量级（0.972 对 0.533）。
5. 可撤销：编辑器 MUST 支持按来源筛选与按来源批量撤销。
6. 不猜：讲稿分段无法解析时 MUST 跳过预填并提示，MUST NOT 猜测绑定。
7. 相对语义：预填 MUST 只写 `withSegment:<segmentId>`，MUST NOT 写绝对毫秒——分段时长随讲稿修订
   变化，绝对时间在改稿后即失效。

#### Scenario: 重跑结果一致

- **WHEN** 对同一页与同一份讲稿分段连续调用两次预填
- **THEN** 两次产出的绑定集合（元素 → 分段、来源、置信度）MUST 逐条相等

#### Scenario: 不覆盖人工绑定

- **WHEN** 用户已手工把某元素绑到 `seg-3`，随后触发预填
- **THEN** 该元素的绑定 MUST 保持 `seg-3` 且来源为 `manual`

#### Scenario: 纯文字页标记低置信

- **WHEN** 某页缺少编号标记、卡片簇与方位信号，预填只能依赖字面与标题先验
- **THEN** 产出的绑定 MUST 标记为低置信，编辑器 MUST 以区别样式呈现

#### Scenario: 分段失效时跳过预填

- **WHEN** 讲稿分段无法解析（分段清单为空或修订失配）
- **THEN** 系统 MUST 跳过该页预填（不产出任何 `withSegment:` 绑定），MUST 在响应里说明跳过原因，
  且这些结果 MUST NOT 被标注为预填产物（`triggerSource` 为空）
- **AND** 该页 MAY 退回既有的预设默认规格（顺序错落 + `onEnter`）——那是「没有绑定信息时的
  默认编排」，不是预填的猜测；两者以响应里的预填说明（`applied=false`）区分
