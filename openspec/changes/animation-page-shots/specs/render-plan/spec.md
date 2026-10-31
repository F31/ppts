# Spec Delta

## ADDED Requirements

### Requirement: 页内镜头切片（Page.Shots）

一页 MAY 声明多个镜头切片，按页内相对时间切换底图。未声明 `shots` 的页 MUST 保持单底图行为，打包产物 MUST NOT 出现 `shots` 字段，画面 MUST 与引入本需求前逐像素一致。

`shots` 非空时 MUST 满足五条：按 `startMs` 升序且首尾相接；MUST NOT 有空洞或重叠；总长 MUST 等于页时长（容差 1 帧）；`crossFadeMs` MUST 短于本镜时长；首镜 MUST NOT 声明 `crop`——首镜就是整页，声明裁切等于说这张整页图是从局部裁出来的。

叠化 MUST 占**后一镜**的开头 `[startMs, startMs+crossFadeMs)`，进度走与页间转场同一条 `easeOutCubic`。

#### Scenario: 未声明 shots 的页行为不变

- **WHEN** 用一份不含 `shots` 的既有包重新打包
- **THEN** 产物 MUST 不含 `shots` 字段，V13 逐页最差与 V13b 三项指标 MUST 与引入前同量级

#### Scenario: 切片留了空洞

- **WHEN** 页时长 4000ms，`shots` 是 0–2000ms 与 2500–4000ms
- **THEN** 打包 MUST 失败，报错 MUST 指出页码与缺失区间（2000–2500ms），MUST NOT 用首段图把空洞填上

#### Scenario: 叠化窗口的右端

- **WHEN** 第二镜 `startMs=2000, crossFadeMs=120`
- **THEN** 2119ms 时 MUST 仍处于叠化（两张图同时可见），2120ms 时 MUST 已经是纯第二镜

### Requirement: 首镜等于页面的呈现底图

首镜的 `image` MUST 等于这一页**画出来是什么样**的那张图：有 `PlateImage` 时是底板，否则是 `BaseImage`。

写成 `BaseImage` 在「有分层动画的页又切镜头」时会产生残影——元素本来就在图里，再从透明淡入一次。两条路径都合法，画面却差一层，所以只能在这里判。

#### Scenario: 首镜指向 baseImage 而这一页有底板

- **WHEN** `PlateImage` 非空，而 `shots[0].image` 是 `BaseImage`
- **THEN** 校验 MUST 失败并指出首镜必须是底板，MUST NOT 以「路径合法」放行

### Requirement: 镜头切换不改变层的时间基准与可见性

镜头切片 MUST NOT 改变层的求值时间基准：`Layer.Clips` 仍是页内相对毫秒，跨过镜头边界时 MUST NOT 重建图层。重建会让层重新入场（`main.ts` 的 `rebuildLayers` 先 `release` 再 `build`），而切镜头在时间轴上只是底图换了，元素并没有结束。

`crop` 非空的镜头内 MUST NOT 显示层：层的 `box` 是整页坐标，特写把原页局部放大后，同一套坐标必然错位。隐藏 MUST 用不改 DOM 的方式（如 `visibility`），以便切回全图时不重建。

#### Scenario: 切镜头不重建层

- **WHEN** 播放器从 1990ms 走到 2010ms，跨过 2000ms 的镜头边界
- **THEN** 底图 MUST 换为第二个镜头的图；同一 `elementId` 的层 DOM 节点 MUST 保持同一个（MUST NOT 被释放后重建）

#### Scenario: 特写镜头内不显示层

- **WHEN** 第二个镜头声明了 `crop`，且该页有元素动画层
- **THEN** 该镜头区间内层 MUST NOT 可见，且层节点 MUST 仍留在 DOM 里

### Requirement: 镜头图的裁切比例在打包期核对

`crop` 非空时，该镜头的位图宽高比与 `crop` 的宽高比之差 MUST ≤1%。比例不符说明裁切算错了：播放器把图铺满舞台，拉伸后的画面「看着还挺正常」，只有把特写与原页并排时才看得出元素变形。

这条 MUST 在打包期做——IR 里 `image` 只是一个路径，量不出像素尺寸；打包时图已经落盘。

首镜的位图 MUST NOT 另存一份：它就是要写进清单的页级图，打包器 MUST 复用同一份字节与同一个文件名。

#### Scenario: 裁切比例算错

- **WHEN** 第 2 镜的图是 800×800，`crop` 是 800×450
- **THEN** 打包 MUST 失败并报出两边的比例，MUST NOT 把图写进包

#### Scenario: 首镜不重复占一份体积

- **WHEN** 一页声明了两个镜头并成功打包
- **THEN** 包内 MUST 只有第 2 镜起的独立文件（`slides/NNN-shot-01.png` …），首镜 MUST 指向页级图，MUST NOT 出现 `slides/NNN-shot-00.png`
