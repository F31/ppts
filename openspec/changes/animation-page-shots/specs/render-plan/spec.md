# Spec Delta

## ADDED Requirements

### Requirement: 页内镜头切片（Page.Shots）

一页 MAY 声明多个镜头切片，按页内相对时间切换底图。未声明 `shots` 的页 MUST 保持单底图行为，打包产物 MUST NOT 出现 `shots` 字段，画面 MUST 与引入本需求前逐像素一致。

`shots` 非空时 MUST 满足三条：按 `startMs` 升序且首尾相接；MUST NOT 有空洞或重叠；总长 MUST 等于页时长（容差 1 帧）。`Page.BaseImage` MUST 等于首个镜头的 `image`——否则「这一页长什么样」有两个说法。

镜头切片 MUST NOT 改变层的求值时间基准：`Layer.Clips` 仍是页内相对毫秒，跨过镜头边界时 MUST NOT 重建图层。重建会让层重新入场（`main.ts` 的 `rebuildLayers` 先 `release` 再 `build`），而切镜头在时间轴上只是底图换了，元素并没有结束。

#### Scenario: 未声明 shots 的页行为不变

- **WHEN** 用一份不含 `shots` 的既有包重新打包
- **THEN** 产物 MUST 不含 `shots` 字段，V13 逐页最差与 V13b 三项指标 MUST 与引入前同量级

#### Scenario: 切片留了空洞

- **WHEN** 页时长 4000ms，`shots` 是 0–2000ms 与 2500–4000ms
- **THEN** 打包 MUST 失败，报错 MUST 指出页码与缺失区间（2000–2500ms），MUST NOT 用首段图把空洞填上

#### Scenario: 切镜头不重建层

- **WHEN** 播放器从 1990ms 走到 2010ms，跨过 2000ms 的镜头边界
- **THEN** 底图 MUST 换为第二个镜头的图；同一 `elementId` 的层 DOM 节点 MUST 保持同一个（MUST NOT 被释放后重建）
