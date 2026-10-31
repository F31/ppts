# Spec Delta

## ADDED Requirements

### Requirement: 镜头相机（Shot.Camera）

镜头 MAY 声明 `camera` 关键帧序列，表示该镜头内的平移/缩放运动（Ken Burns）。`camera` 为空时
画面 MUST 与引入本需求前逐像素一致（无相机时退化为静态特写）。

`camera` 关键帧 MUST 表达为镜头图局部的归一化坐标：`x` / `y` 为视口中心在镜头图上的归一化
位置（0.5 = 中心），`scale` MUST 满足 `scale ≥ 1`，为相对「图覆盖舞台」的缩放倍率；
`atMs` 为镜内相对毫秒，关键帧 MUST 按 `atMs` 升序。

`x` / `y` 的**合法区间由 scale 推导**：要让缩放后镜头图始终盖住舞台、不露白，视口中心 MUST 落在
`[0.5/scale, 1-0.5/scale]`（scale=1 退化为单点 0.5；scale=2 为 [0.25,0.75]）。校验 MUST 拒绝
落在安全区间外的关键帧；`CameraTransform` 运行时再钳制一次，作为防御（即便输入越界也只裁掉
越界部分而非露白）。注意 `[0,1]` 是归一化存储范围，**不是**安全区间——单纯 x/y∈[0,1] 不足以保证
不露白。

播放器 MUST 在相邻关键帧间线性插值（首帧前 / 末帧后钳制到端点），并应用 `translate + scale`
变换到镜头图，舞台 `overflow:hidden` 裁剪。校验 MUST 保证 `scale ≥ 1` 且中心落在覆盖安全区间：
否则缩放后镜头图无法覆盖舞台，会在边缘露出空白。

声明了 `camera` 的镜头 MUST 同时声明 `crop`——相机在特写图内部运动；无 `crop` 的整页相机不在
本需求范围。

#### Scenario: 无相机的镜头行为不变

- **WHEN** 用一份不含 `camera` 的既有包重新打包
- **THEN** 产物 MUST 不含 `camera` 字段，画面 MUST 与引入本需求前逐像素一致

#### Scenario: 相机变换后镜头图覆盖舞台

- **WHEN** `scale ≥ 1` 且相机中心落在覆盖安全区间 `[0.5/scale, 1-0.5/scale]`（或越界经 `CameraTransform` 钳制后落入该区间）的相机关键帧
- **THEN** 变换后镜头图四边 MUST 不内缩于舞台，`overflow:hidden` 只裁多余部分，MUST NOT 露白

#### Scenario: scale < 1 被校验拒绝

- **WHEN** 某个相机关键帧 `scale < 1`
- **THEN** 编译/打包 MUST 记录诊断并拒绝该镜头，MUST NOT 输出会让舞台露白的相机

#### Scenario: 相机镜头必带 crop

- **WHEN** 一个镜头声明了 `camera` 但未声明 `crop`
- **THEN** 打包 MUST 记录诊断并拒绝，MUST NOT 产出无特写的相机镜头
