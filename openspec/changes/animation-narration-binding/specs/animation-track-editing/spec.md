# Spec Delta

## ADDED Requirements

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
