# Spec Delta

## Purpose

把 HTML 成品包从「有声幻灯」补成「与 MP4 同一份 IR 的第二个渲染目标」：包内必须带得起元素级动画所需的分层资源，播放器必须用音频时钟驱动与 MP4 同语义的关键帧，且两端像素一致到可测。这条目的不是「让 HTML 能动画」，而是「让用户拿到的两个产物是同一支片子的两种形态」。

## ADDED Requirements

### Requirement: 成品包携带分层资源与动画数据

当动画开关开启时，包 MUST 为每个含 clip 的元素提供独立位图（只含该元素、已按其 `box` 裁切），并把该页的 `layers[*].clips` 原样写入清单。底板（挖空元素本体后的页面图）MUST 在同一份原始页图上生成，MUST NOT 在降采样或 JPEG 转码后的图上做取色。

`layers` 数据 MUST 与渲染计划里的 IR 字段一一对应，MUST NOT 在导出期做部分预编译（属性展开、clip 合并等）后写入——预编译会引入第二套插值实现。

#### Scenario: 有元素动画的页

- **WHEN** 某页存在 `layers[*].clips` 非空
- **THEN** 清单该页 `layers` 中每项都有 `image`（包内路径）、`box`（画布像素）、`clips`（IR 原样），
  且 `image` 指向的位图只含该元素像素
- **AND** 该页 `image` 指向挖空底板

#### Scenario: 无元素动画的页

- **WHEN** 某页 `layers` 为空
- **THEN** 该页 `image` 为完整页面底图，`layers` 为空数组或省略，播放器按 M6-1 的静态逻辑处理

### Requirement: DOM 动画以音频为权威时钟并由 WAAPI 回放

播放器 MUST 以 `audio.currentTime` 作为动画的唯一时间源，MUST NOT 用 `requestAnimationFrame` 自行累计时间。关键帧 MUST 在首次进入该页时编译一次，回放阶段 MUST 只通过 WAAPI 的 `play/pause/currentTime` 驱动，MUST NOT 在播放循环里逐帧写 `style`。

clip 的叠加语义 MUST 与渲染端一致：`opacity` 相乘、`translate*` 相加、`scale*` 相乘。同属性多条 clip 时间重叠时，MUST 在该区间内按统一求值函数采样合并，MUST NOT 依赖 WAAPI 默认的 `replace` 语义。

#### Scenario: 播放中 seek 到动画进行中的时刻

- **WHEN** 用户在某一页动画进行中拖动进度条
- **THEN** 该页所有 animation 先被批量 `pause()` 并设到目标 `currentTime`，再按原播放状态恢复，
  画面与同时刻 MP4 帧的位置/不透明度一致，且不出现闪烁

#### Scenario: 切页

- **WHEN** 播放进入下一页
- **THEN** 上一页全部 animation 被 `cancel()`，不残留在合成层

#### Scenario: 同一元素两条重叠 clip

- **WHEN** 某元素存在两条时间区间重叠、同属性的 clip
- **THEN** 编译期按 `evaluateLayer` 采样合并为一条 keyframe 序列，
  在重叠区间内该时刻的数值等于渲染端同一函数的结果

### Requirement: 两端同帧 SSIM 不低于 0.95

验收 MUST 在**动画进行中**取帧：每页取第一条 clip 的中点时刻，另加首帧与末帧。判定 MUST 用逐页最差帧，MUST NOT 用全片平均。

比较基准 MUST 先量化 H.264 编码损失：若同一张源图编解码后的 SSIM 天花板本身低于阈值，门禁 MUST 改为与「同一段静态画面的编码帧」比较，而不是与未编码源图比较。

#### Scenario: 动画中点帧

- **WHEN** 对某页 `startMs + durationMs/2` 时刻，MP4 解码帧与浏览器截图比较
- **THEN** SSIM ≥ 0.95

#### Scenario: 某一页不达标

- **WHEN** 任一页任一取样时刻 SSIM < 0.95
- **THEN** 变更 MUST NOT 通过验收，且报告必须指出该页该时刻，而不是只给一个全片均值

### Requirement: 动画关闭时零副作用

动画开关关闭时，包内 MUST NOT 含 `layers` 或非 `none` 的 `transition`，产物 MUST 与 M6-1 的包在画面内容上逐像素一致（`createdAt`/`snapshotHash` 这类天然变化的字段除外）。

#### Scenario: 关闭开关导出

- **WHEN** 动画开关关闭
- **THEN** 清单 `schemaVersion` 仍为 2，但所有页 `layers` 为空、`transition.type` 为 `none`
- **AND** 解出的页面图与 M6-1 产物逐像素相同

### Requirement: 动画能力缺失时降级为可播放

播放器在缺 `Element.animate`、`prefers-reduced-motion: reduce`、清单版本高于支持版本三种情况下 MUST 仍可播放，MUST NOT 抛错或停在封面。

`prefers-reduced-motion` 下 MUST 保留 opacity 变化、去掉位移与缩放，且**时长不变**——缩短时长会让动画提前结束、与音频和字幕错位。

#### Scenario: 环境无 WAAPI

- **WHEN** `Element.prototype.animate` 不存在
- **THEN** 播放器按静态切页播放完整时长，不抛异常

#### Scenario: 清单版本过高

- **WHEN** 读到 `schemaVersion` 大于播放器支持的最大版本
- **THEN** 播放器明确提示「需要更新播放器」，而不是按旧版逻辑猜着播
