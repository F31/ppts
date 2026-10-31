# Spec Delta

## MODIFIED Requirements

### Requirement: 页取样门禁覆盖跨页延续

既有页取样门禁（V13/V13b/V18 等按页取末帧比对）的结构 MUST 保持不变，且 MUST NOT 把延续元素纳入
页末帧比对：延续元素在页尾本就处于中途态，混入「末帧 == 终态参照图」的判据会产生假红。

跨页延续的判据 MUST 落在**成品包清单 + 共享求值器**上：对包内每个 `isContinuation = true` 的 clip，
门禁 MUST 断言下列全部条件，任一不满足即判失败。

1. `elementId` 存在，且等于所在层的 `elementId`。
2. `continuationOffsetMs ≥ 0` 且 `startMs = -continuationOffsetMs`。
3. 延续段不出现在首屏。
4. 前一页存在配对的源段：同 `elementId`、同 `effect`、同 `durationMs`、同 `Props`，且
   `startMs = 前一页页长 - continuationOffsetMs`。
5. 用与播放器、渲染器同一份 `shared/motion.ts` 在页缝两侧求值（前一页页尾 vs 次页 0 点），
   `opacity` / 位移 / 缩放之差 MUST ≤ 1e-6。
6. 页缝处的 clip 进度 MUST 落在 (0, 1) 开区间——否则该 clip 并未真正跨页，判据形同虚设。

门禁 MUST 在包内**没有任何延续段**时判失败（空跑通过等于没有门禁）。跨页延续夹具 MUST 由显式开关产出
（`share-baseline -continuation`），MUST NOT 默认挂在分层素材开关上；开关的前置条件（分层 > 0、页数 ≥ 2、
页长大于偏移量）不满足时 MUST 报错退出，MUST NOT 静默跳过。

#### Scenario: 延续元素在页缝处视觉连续

- **WHEN** 用含跨页延续元素的包在页缝处比对（前一页页尾 vs 次页首帧）
- **THEN** 同 `elementId` 元素的位置/不透明度 MUST 在容差内一致，MUST NOT 出现跳变或首帧缺失

#### Scenario: 延续段链接字段自洽且页缝连续

- **WHEN** 对含跨页延续元素的成品包运行页缝门禁
- **THEN** 每个延续段 MUST 通过上述 1–6 条，页缝两侧求值差 MUST ≤ 1e-6

#### Scenario: 延续段被写成正起点

- **WHEN** 清单中某延续段的 `startMs` 为正数（等于 `continuationOffsetMs` 而非其相反数）
- **THEN** 门禁 MUST 判失败，并指出该段 `startMs` 与 `offset` 的实际取值

#### Scenario: 源段与延续段 Props 不一致

- **WHEN** 延续段与前一页配对源段的 `Props`（属性名或 from/to）不一致
- **THEN** 门禁 MUST 判为「未找到配对源段」并失败

#### Scenario: 夹具退回导致没有延续段

- **WHEN** 包里不存在任何 `isContinuation = true` 的 clip
- **THEN** 门禁 MUST 判失败（MUST NOT 因为「无对象可查」而通过）

#### Scenario: 既有非延续页面门禁不受影响

- **WHEN** 对不含延续元素的既有 fixture 跑 V13/V13b/V17/V18
- **THEN** 所有既有判据 MUST 仍通过，行为与引入本需求前一致
