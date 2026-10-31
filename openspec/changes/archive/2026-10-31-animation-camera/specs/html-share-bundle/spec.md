# Spec Delta

## ADDED Requirements

### Requirement: 镜头相机字段为可选且不升 schemaVersion

声明了页内镜头的页，`slides[i].shots[]` 的每一项 MAY 另带 `camera[]`（相机关键帧），语义与
IR 的 `render.CameraKeyframe` 同构，字段为 `atMs`、`x`、`y`、`scale`。`camera` 是可选字段：
缺省时产物 MUST 与引入它之前逐字节一致。

`camera` 非空时，其所属镜头 MUST 同时声明 `crop`（相机在特写图内部运动）。打包 MUST 校验
`scale ≥ 1` 与中心落在覆盖安全区间 `[0.5/scale, 1-0.5/scale]`、关键帧按 `atMs` 升序；任一违例
MUST 记录诊断并拒绝该镜头。（注：`[0,1]` 只是归一化存储范围，并非安全区间。）

`schemaVersion` MUST NOT 因为引入 `camera` 而升高：旧播放器忽略 `camera` 字段，停在首镜图
（`slides[i].shots[0].image` 已由校验绑死为 `slides[i].image`），真实表现为「降级可播放、但不运
动」——规格 MUST 如实这么写，MUST NOT 声称「已兼容运动」。

#### Scenario: 无相机的包逐字节一致

- **WHEN** 用一份不含 `camera` 的既有包重新打包
- **THEN** 产物 MUST 不含 `camera` 字段，且 MUST 与引入本需求前逐字节一致

#### Scenario: 带相机的镜头写出 camera 字段

- **WHEN** 清单里某镜头声明了 `camera`
- **THEN** 包内 `shots[k]` MUST 含 `camera[]`，每项含 `atMs`/`x`/`y`/`scale`，且 `crop` 非空

#### Scenario: 相机镜头缺 crop 被拒

- **WHEN** 某镜头含 `camera` 但 `crop` 为空
- **THEN** 打包 MUST 记录诊断并拒绝该镜头，MUST NOT 产出无特写的相机镜头

#### Scenario: 引入 camera 不升 schemaVersion

- **WHEN** 包内含 `camera` 字段
- **THEN** `schemaVersion` MUST 保持不变，旧播放器忽略 `camera` 停在首镜且不报错
