# Spec Delta

## ADDED Requirements

### Requirement: 页取样门禁覆盖跨页延续

既有页取样门禁（V13/V13b/V18 等按页取末帧比对）的结构 MUST 保持不变。本需求新增一个「跨页延续」fixture /
case：一个元素在相邻两页之间以 `continues = true` 延续，门禁 MUST 断言在**页缝处**（本页末帧 vs 次页首帧）
该同 `elementId` 元素的位置与不透明度在容差内连续（无跳变、无消失），以此守住「延续段不重置」的回归。

非延续页面（绝大多数既有 fixture）的取样判据 MUST 不受影响；延续 case 是增量覆盖，MUST NOT 改变既有
按页取样的假设。

#### Scenario: 延续元素在页缝处视觉连续

- **WHEN** 用含跨页延续元素的包在页缝处抽本页末帧与次页首帧
- **THEN** 同 `elementId` 元素的位置/不透明度 MUST 在容差内一致，MUST NOT 出现跳变或首帧缺失

#### Scenario: 既有非延续页面门禁不受影响

- **WHEN** 对不含延续元素的既有 fixture 跑页取样门禁
- **THEN** 所有既有按页取样判据 MUST 仍通过，行为与引入本需求前一致
