# Spec Delta

## MODIFIED Requirements

### Requirement: 包结构与清单契约

包内 MUST 包含 `manifest.json`，其 MUST 声明 `schemaVersion`、`video{width,height,fps}`、`slides[{index,image,startUS,durationUS}]`、`audio` 片段列表、`subtitles.src`、`artifact.durationUS`。清单中的路径 MUST 为包内相对路径，MUST NOT 出现绝对路径或对象存储 key。

`slides[i]` MAY 另带 `shots[]`（页内镜头切片）与 `layers[]`（元素动画层）。两者都是可选字段：缺省时产物 MUST 与引入它们之前逐字节一致。

#### Scenario: 清单自洽

- **WHEN** 校验器读取 `manifest.json`
- **THEN** 每个 `slides[i].image` 与音频条目 MUST 在包内存在，且 `slides` 时间累加与 `artifact.durationUS` 误差 ≤ 100ms

#### Scenario: 拒绝绝对路径

- **WHEN** 打包时发现某资源路径以 `/`、`http`、`file:` 或对象存储 key 形式出现
- **THEN** 打包 MUST 失败并报出该资源，MUST NOT 生成带外链的包

## ADDED Requirements

### Requirement: 页内镜头切片写入清单

声明了页内镜头的页，清单 MUST 写出 `slides[i].shots[]`，每项含 `index`、`image`、`crop?`、`startMs`、`durationMs`、`crossFadeMs`，语义与 IR 的 `render.Shot` 同构。首镜 MUST 复用该页的页级图（同一份字节、同一个包内路径），MUST NOT 为它多写一个文件；第 2 镜起 MUST 写成独立文件（`slides/NNN-shot-0K.png`）。

#### Scenario: 首镜复用页级图

- **WHEN** 一页声明两镜，首镜 `image` 等于 `slides[i].image`
- **THEN** 包内 MUST 只有一个与页级图对应的文件，`shots[0].image` MUST 是它的路径

#### Scenario: 首镜不等于页级图

- **WHEN** 清单里 `shots[0].image` 与 `slides[i].image` 不同
- **THEN** 校验 MUST 失败并指出两者，MUST NOT 让「这一页长什么样」有两个说法

### Requirement: 清单的镜头校验独立于 IR 校验

产物侧 MUST 独立于 IR 再校验一遍：首镜 `image` 等于 `slide.image`、`index` 等于位置、时间轴首尾相接且总长等于页时长（±1 帧）、`crossFadeMs` 短于本镜、`crop` 在原页范围内、每个 `shot.image` 在包内存在。

重复一遍不是冗余：门禁对的是**产物**——清单可能被手改，也可能由别的工具生成，不能假设它一定出自本仓的编译器。

#### Scenario: 手改清单删掉一镜

- **WHEN** 手工把 `shots` 里中间一镜删掉，留下时间空洞
- **THEN** 校验 MUST 失败并指出页码与缺失区间，MUST NOT 让包在「少一镜」的状态下通过

### Requirement: 裁切比例只能在打包期核

`crop` 的宽高比与 `image` 的像素宽高比之差 MUST ≤1%。这条 MUST 放在打包期：IR 里 `image` 只是路径，量不出像素尺寸；图落盘之后才能量。

比例算错时浏览器会把图拉伸，而拉伸后的画面「看着还挺正常」——所以它必须在能读到图的那一层拦下。

#### Scenario: 裁切比例算错

- **WHEN** `crop` 是 640×360（16:9），而落盘的 `image` 是 640×480（4:3）
- **THEN** 打包 MUST 失败并报出页码与镜号，MUST NOT 写出会被浏览器拉伸的包

### Requirement: 引入 shots 不升 schemaVersion

`schemaVersion` MUST NOT 因为引入 `shots` 而升高。依据是证据不是直觉：播放器不读这个字段，升版本只会让 Go 的校验器拒绝旧包，客户端行为一点没变。

`shots` 是可选字段，旧播放器会忽略它、停在首镜图（`slide.image` 已由校验绑死为 `shots[0].image`）。所以真实表现是「降级可播放、但不换镜」——规格 MUST 如实这么写，MUST NOT 声称「已兼容」。

#### Scenario: 旧播放器读到 shots

- **WHEN** 用引入镜头之前的播放器打开一份带 `shots` 的包
- **THEN** 包 MUST 仍可播放，画面停在首镜图；这不是兼容，是降级可播放
