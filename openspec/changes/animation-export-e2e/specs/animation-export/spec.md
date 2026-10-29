# Spec Delta

## Purpose

定义动画视频的导出通道：动画能力如何接入既有导出流程、失败时如何降级、以及「关闭开关」必须等价于「没有这个能力」。目标是让「动画是增强，不是前置条件」这条原则成为可执行的断言，而不是部署时的口头约定。

## ADDED Requirements

### Requirement: 动画通道失败必须降级，不得阻断导出

动画渲染的任何失败（服务不可达、健康检查失败、全片渲染失败、超时）MUST 被吸收为降级，导出 MUST 继续走静态路径并产出可播放的 MP4。动画通道的错误 MUST NOT 进入静态编码的错误分类器，MUST NOT 使导出任务失败。

降级 MUST 可被用户感知：成品 MUST 携带降级标记，接口 MUST 能说明「已回退静态版」或「第 N 页用静态画面替代」。

#### Scenario: 渲染服务不可达

- **WHEN** 渲染服务未部署或 `/healthz` 失败，用户导出动画视频
- **THEN** 导出成功并产出静态版 MP4，响应中标记已降级且原因可读

#### Scenario: 全片渲染失败

- **WHEN** 渲染任务返回 failed（非单页错误）
- **THEN** 导出回退整片静态，任务状态为成功，不出现「导出失败」

### Requirement: 单页失败只替换该页，时间轴逐页对齐不变

单页渲染失败（可重试耗尽）MUST 只把该页替换为静态画面段，其余页 MUST 保留动画。成片总时长 MUST 与时间轴一致（误差 ≤ 100ms），音频与字幕 MUST NOT 因该页替换而整体前移或后移。

#### Scenario: 第 3 页渲染失败

- **WHEN** 5 页中第 3 页渲染失败，其余 4 页成功
- **THEN** 成片仍为 5 页时长之和，第 3 页为静态画面，且用户可得知是第 3 页降级

#### Scenario: 该页被跳过（反例）

- **WHEN** 成片时长比时间轴短一页
- **THEN** 视为违反本要求：音画会整体错位，不允许以「跳过」实现降级

### Requirement: 拼接只取视频流，并校验逐段时长

拼接 MUST 只取各段的视频流（`-map 0:v:0`），MUST NOT 把降级段自带的静音音轨拼入成片。成片音轨 MUST 来自时间轴音频。

拼接前 MUST 对每段取实际时长；与计划时长累计偏差超过 100ms 的页 MUST 被替换为静态段后再拼接。

#### Scenario: 降级段带静音轨

- **WHEN** 某页降级段由静态图编码而来（自带 `anullsrc` 静音轨）
- **THEN** 成片只有一条来自时间轴的音轨，且该页不出现「有声音但全是静音」

#### Scenario: 分段时长漂移

- **WHEN** 某段实际时长比计划长 150ms
- **THEN** 该页改用静态段拼接，成片总时长与时间轴误差回到 100ms 内

### Requirement: 关闭开关时导出与无此能力时等价

`animation.enabled` 关闭时，导出 MUST NOT 探测渲染服务、MUST NOT 提交渲染任务、MUST NOT 改变静态编码参数。

导出快照的序列化结果 MUST 与引入动画能力之前逐字节一致（动画字段必须省略），以保证 `snapshotHash` 不变、既有产物仍可命中。

#### Scenario: 开关关闭后重复导出

- **WHEN** 同一项目在开关关闭状态下导出 MP4
- **THEN** 不产生任何渲染服务请求，`snapshotHash` 与引入动画前相同，产物可复用

#### Scenario: 开关从开到关

- **WHEN** 先开启动画导出一次，再关闭后导出同一项目
- **THEN** 第二次导出走静态路径且成功，不回滚或删除第一次的产物

### Requirement: 预览只渲染被请求的页

单页预览 MUST 只提交该页的 IR（1 页），MUST NOT 触发全片渲染。预览超时 MUST 返回明确错误，MUST NOT 以空结果或无限等待收场。

#### Scenario: 预览第 3 页

- **WHEN** 用户在编辑器中请求预览第 3 页
- **THEN** 渲染服务只收到 1 页的计划，且返回该页片段

#### Scenario: 预览超时

- **WHEN** 渲染服务在单页预算内未返回
- **THEN** 接口返回超时错误并提示可重试，编辑器不得显示空白预览

### Requirement: 导出请求必须显式携带动画规格身份

导出入口 MUST 接受逐页的动画规格身份（`slideId` / `language` / `sourceRevisionNo` / `revision`），MUST NOT 由后端在导出时自行取「当前最新版」。规格身份 MUST 进入 `ExportSnapshot` 并参与 `snapshotHash`，使「排队期间规格被改」表现为另一份产物而不是复用旧键。

空规格集合 MUST 与不传该字段得到同一个快照键（两者都不渲染动画，产物同源）。

#### Scenario: 排队期间规格被改

- **WHEN** 导出任务入队后、执行前，某页规格的 `revision` 从 3 变成 4
- **THEN** 该页按无规格处理（静态画面），导出成功，日志记录 `snapshot_revision=3 stored_revision=4`

#### Scenario: 规格身份不合法

- **WHEN** 请求里的 `slideId` 为空、在同一请求里重复、或 `revision` ≤ 0
- **THEN** 入口返回 400 并指出具体是第几项，MUST NOT 创建任务

#### Scenario: 对非视频格式请求动画

- **WHEN** `format=html_bundle`（或其它不产视频的格式）携带 animation
- **THEN** 入口返回 400：接受它等于承诺一个做不到的效果

### Requirement: 导出前必须能回答「这次能不能出动画」

系统 MUST 提供一个导出选项探测端点，返回三段能力判定（规格存储可用 / 部署开关开启 / 渲染服务此刻可达）与逐页规格身份。任一段不满足时 MUST 返回可区分的原因码，MUST NOT 只返回一个 false。

能力判定 MUST NOT 复用导出主路径的进程级健康缓存：导出主路径要的是「不因抖动来回切」，而探测端点要的是「现在能不能用」——服务恢复后不重启也必须变绿。

#### Scenario: 渲染服务停掉后重新打开导出对话框

- **WHEN** 渲染服务不可达，用户打开导出对话框
- **THEN** 端点返回 `enabled=false` 且 reason 以 `renderer_unavailable` 开头，前端把「动画视频」置灰并说明原因

#### Scenario: 服务恢复

- **WHEN** 渲染服务恢复，用户重新打开导出对话框（探测缓存已过期）
- **THEN** 端点返回 `enabled=true`，无需重启 API 进程

#### Scenario: 部分页没有规格

- **WHEN** 时间轴有 12 页，只有 5 页配了规格
- **THEN** 端点返回 12 项，其中 5 项 `revision>0`、7 项 `revision=0`；前端展示「5/12 页配了动效」而不是让用户自己数

### Requirement: 两个导出入口对同一格式语义一致

REST 导出入口与 Connect 导出入口 MUST 对同一格式产出同一结构的 `ExportSnapshot`（含 `burnSubtitles` / `includeNotes` / `animation`）。MUST NOT 存在「同一份输入从不同入口进来得到不同产物」的分支。

#### Scenario: mp4 从 REST 入口导出

- **WHEN** 用户带 `burnSubtitles=true` 从 REST 入口导出 mp4
- **THEN** 快照里 `burnSubtitles=true`，与 Connect 入口同参数导出的字幕行为一致
