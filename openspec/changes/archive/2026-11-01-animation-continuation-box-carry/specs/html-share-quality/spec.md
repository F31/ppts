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
   `startMs = 前一页页长 - continuationOffsetMs`；链式时配对段可以是上一跳延续段，其期望偏移量为
   `上一跳 continuationOffsetMs + 前一页页长`。`carry = true` 的段 MUST 被排除出配对。
5. 用与播放器、渲染器同一份 `shared/motion.ts` 在页缝两侧求值（前一页页尾 vs 次页 0 点），
   `opacity` / 位移 / 缩放之差 MUST ≤ 1e-6。比较 MUST 排除 `carry = true` 的段——补偿段刻意让两侧
   的位移/缩放不同，混入本判据会互相抵消。
6. 页缝处的 clip 进度 MUST 落在 (0, 1) 开区间——否则该 clip 并未真正跨页，判据形同虚设。
7. 页缝两侧的**视觉矩形**之差 MUST ≤ 1e-6。视觉矩形是层的 `box` 叠加 `transform` 后的绝对几何：
   `左上 = box + O·(1-s) + t`、`尺寸 = (s·w, s·h)`；该比较 MUST **包含** carry 补偿段。
8. 每个 `carry = true` 段的属性终点 MUST 为恒等变换（`opacity` 与 `scale*` 为 1，`translate*` 为 0），
   且 MUST NOT 标记 `isContinuation`。
9. 包内 MUST 至少存在一条 `carry = true` 的段——延续夹具必须让相邻页的静态盒不同，否则第 7 条会
   因为「两侧盒本来相等」而恒真。

门禁 MUST 在包内**没有任何延续段**时判失败（空跑通过等于没有门禁）。跨页延续夹具 MUST 由显式开关产出
（`share-baseline -continuation`），MUST NOT 默认挂在分层素材开关上；开关的前置条件（分层 > 0、页数 ≥ 2、
页长大于偏移量）不满足时 MUST 报错退出，MUST NOT 静默跳过。

#### Scenario: 延续元素在页缝处视觉连续

- **WHEN** 用含跨页延续元素的包在页缝处比对（前一页页尾 vs 次页首帧）
- **THEN** 同 `elementId` 元素的位置/不透明度 MUST 在容差内一致，MUST NOT 出现跳变或首帧缺失

#### Scenario: 延续段链接字段自洽且页缝连续

- **WHEN** 对含跨页延续元素的成品包运行页缝门禁
- **THEN** 每个延续段 MUST 通过上述 1–9 条，页缝两侧的求值差与视觉矩形差 MUST 均 ≤ 1e-6

#### Scenario: 延续段被写成正起点

- **WHEN** 清单中某延续段的 `startMs` 为正数（等于 `continuationOffsetMs` 而非其相反数）
- **THEN** 门禁 MUST 判失败，并指出该段 `startMs` 与 `offset` 的实际取值

#### Scenario: 相邻页盒不同但缺少补偿段

- **WHEN** 延续元素在相邻两页的静态盒不同，而清单中没有对应的 `carry = true` 段
- **THEN** 门禁 MUST 判失败，并报出页缝两侧的视觉矩形与实际差值（x / y / w / h）

#### Scenario: 补偿量偏离正确值

- **WHEN** `carry` 段的位移起点与「按盒差 + 锚点项」算出的值不一致（例如漏掉 `O·(1-s)`）
- **THEN** 门禁 MUST 判失败，并报出该处视觉矩形的差值

#### Scenario: 补偿段未收敛到恒等变换

- **WHEN** 某个 `carry` 段的属性终点不是恒等值（位移非 0 或缩放非 1）
- **THEN** 门禁 MUST 判失败，并指出该属性名、实际终点值与期望值

#### Scenario: 夹具退回「相邻页同盒」

- **WHEN** 包内存在延续段，但没有任何 `carry = true` 的段
- **THEN** 门禁 MUST 判失败（第 9 条）——盒差没进夹具时，第 7 条会因为两侧盒本来相等而恒真

#### Scenario: 源段与延续段 Props 不一致

- **WHEN** 延续段与前一页配对源段的 `Props`（属性名或 from/to）不一致
- **THEN** 门禁 MUST 判为「未找到配对源段」并失败

#### Scenario: 夹具退回导致没有延续段

- **WHEN** 包里不存在任何 `isContinuation = true` 的 clip
- **THEN** 门禁 MUST 判失败（MUST NOT 因为「无对象可查」而通过）

#### Scenario: 既有非延续页面门禁不受影响

- **WHEN** 对不含延续元素的既有 fixture 跑 V13/V13b/V17/V18
- **THEN** 所有既有判据 MUST 仍通过，行为与引入本需求前一致
