# Spec Delta

## ADDED Requirements

### Requirement: 跨页延续的静态盒差在编译期补偿

跨页延续只保证 clip 进度在页缝处连续，不保证**视觉几何**连续：同一 `elementId` 的元素在相邻页的
静态盒（`box.x/y/w/h`）可以不同。编译器 MUST 在下列条件同时成立时，为承接页的续播层补一条
`carry = true` 的补偿 Motion，把视觉矩形从上一页的盒搬回本页的盒：

- 承接页存在同 `elementId` 层；
- 上一页（首跳为源页，链式时为上一跳所在页）的同 `elementId` 层盒与本页盒**不完全相同**；
- 存在 ≥1ms 的收敛窗口。

补偿量 MUST 按两端共用的 CSS 合成语义计算（`translate` 相加、`scale` 相乘、`transform-origin`
决定缩放锚点），其中 `prev` 为上一页的盒、`next` 为本页的盒：

- `scaleX.from = prev.w / next.w`、`scaleY.from = prev.h / next.h`，二者 `to` MUST 为 1；
- `translateX.from = (prev.x - next.x) - Ox·(1 - scaleX.from)`、
  `translateY.from = (prev.y - next.y) - Oy·(1 - scaleY.from)`，二者 `to` MUST 为 0；
- `Ox/Oy` 为本页层锚点在层内的局部像素偏移（`center` 缺省，另有 `top` / `bottom` / `left` / `right`）。

锚点项 `O·(1-s)` MUST NOT 省略：`scale` 绕 origin 进行，尺寸变化会把原点之外的部分一起带走。
恒等分量（`scale` 1→1、`translate` 0→0）MUST NOT 写入。

补偿段 MUST 满足：`startMs = 0`、`carry = true`、`elementId = Track.element_id`、
`durationMs = min(500, clip 在本页的剩余时长, 本页时长)`，且 MUST NOT 标记 `isContinuation`
——它不是原 clip 的另一截，参与「源段配对」会产生假阳性。

两个盒逐字段相同时，编译器 MUST NOT 产出补偿段：作者按既有写法把元素放在同一位置时，产出的清单
MUST 与引入本能力前逐字节一致（`carry` 为 `omitempty`）。

补偿 MUST 只覆盖盒差（位置与尺寸），MUST NOT 尝试插值内容（图案/颜色/样式）——那属于「换了元素」，
不属于「同一元素在动」。

#### Scenario: 相邻页把元素挪了位置并改了尺寸

- **WHEN** 元素的 clip 跨页延续，其在页 N 的盒为 `(96,54,960,540)`，在页 N+1 的盒为 `(150,80,800,450)`，
  锚点为 `center`
- **THEN** 编译器 MUST 为页 N+1 的续播层补一条 carry 段，其 `scaleX`/`scaleY` 起点为 `1.2`/`1.2`，
  `translateX`/`translateY` 起点为 `26`/`19`，终点分别为 `1`/`1` 与 `0`/`0`

#### Scenario: 相邻页盒完全相同

- **WHEN** 元素在相邻两页的盒逐字段相同
- **THEN** 编译器 MUST NOT 产出 carry 段，该层段数与引入本能力前一致

#### Scenario: 链式延续按上一跳所在页的盒补偿

- **WHEN** 一个 `continues = true` 的 clip 跨三页，且三页的盒互不相同
- **THEN** 第 2 跳的 carry MUST 以**第 2 页的盒**（而非源页的盒）为起点参照，两跳的补偿量 MUST NOT 相等

#### Scenario: 剩余时间不足以收敛

- **WHEN** 延续段在本页的剩余时间不足 500ms
- **THEN** carry 的 `durationMs` MUST 收敛到该剩余时间，且 MUST NOT 超出本页时长
