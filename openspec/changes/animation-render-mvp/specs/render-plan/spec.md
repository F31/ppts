# Spec Delta

## Purpose

定义目标无关的渲染输入 `CompiledTimeline`（IR）、规格到 IR 的编译规则、渲染服务契约与客户端重试语义。IR 是 MP4 与 HTML 两个后端的唯一事实源：它只携带可插值数值，后端不解释动效名。

## ADDED Requirements

### Requirement: IR 不含动效名，只含可插值数值

`CompiledTimeline` MUST 是目标无关的渲染输入。每个动效 MUST 已被展开为「属性 → 起点值/终点值」加一条 `cubic-bezier` 缓动；属性名 MUST 限定在 `opacity`、`translateX`、`translateY`、`scale` 之内。

规范 MUST NOT 要求渲染后端识别动效名、参数名或讲稿语义。`motion.effect` 字段 MUST 仅作审计留痕保留，渲染后端 MUST NOT 读它来决定行为。

#### Scenario: 同一份 IR 被两个后端插值

- **WHEN** 同一份 IR 交给两个独立后端（帧渲染器 / DOM 编译器）
- **THEN** 两者在任意时刻 t 的画面差异只来自渲染精度，不来自动效解释

#### Scenario: 新增动效不需要改后端

- **WHEN** catalog 新增一种入场动效
- **THEN** 只需在 `internal/animation/plan.go` 增加该动效的属性展开，IR 结构与后端零改动

### Requirement: 几何换算只做一次

页面盒 MUST 在编译期由 EMU 换算成目标画布像素，且 MUST 落在 `[0, Width] × [0, Height]` 内。换算 MUST 使用调色板统一比例（`px = emu × canvas / slideEMU`），不得对 X/Y 分别使用不同比例。

#### Scenario: 越界盒被钳制

- **WHEN** 元素 `Bounds` 超出源页范围（解析器给出了异常几何）
- **THEN** 编译器钳制到画布内，并记录 `box_clamped` 诊断；MUST NOT 静默输出负宽高

#### Scenario: 无几何元素被跳过

- **WHEN** 轨道对应的元素在页内不存在或没有 `Bounds`
- **THEN** 该轨道被丢弃并记录 `anchor_missing` 诊断；其余轨道 MUST 照常编译

### Requirement: 时间在编译期解析为页内绝对毫秒

`withSegment:<id>` MUST 被解析为该分段相对本页起点的毫秒偏移；`atTime:<ms>` MUST 直接取值；`onEnter` MUST 为 0。解析后 MUST 满足 `0 ≤ startMs` 且 `startMs + durationMs ≤ pageDurationMs`。

无法解析的分段 MUST 记录 `segment_unresolved` 诊断并把该 clip 的起点退化为 0，MUST NOT 静默挪到页尾或丢弃整页。

#### Scenario: 分段锚定落在页中

- **WHEN** 某页从全局 12000ms 开始，第 3 段从全局 15500ms 开始，clip 触发为 `withSegment:seg-3`
- **THEN** IR 中该 clip 的 `startMs` 为 3500

#### Scenario: 锚定失效

- **WHEN** `withSegment:seg-9` 在当前讲稿修订中不存在
- **THEN** 该 clip 的 `startMs` 为 0，IR 带 `segment_unresolved` 诊断，页面仍可渲染

### Requirement: 强度只缩放幅度

编译期 MUST 用 `intensity` 线性缩放位移量与缩放幅度（`scale` 的 `1-from` 差值），MUST NOT 改变 `durationMs`。

#### Scenario: 强度不改时长

- **WHEN** spec 中 clip 为 `slideIn`、`durationMs=600`、`offset=40`，页 `intensity=1.5`
- **THEN** IR 中 `durationMs` 仍为 600，`translateY.from` 为 60

### Requirement: 页转场由预设决定且不进入可编辑规格

每页 MUST 携带 `transition{type,durationMs}`。首屏 MUST 为 `none`（没有前一页可交叉）。转场类型 MUST 限定在 `none|fade|slide|zoom`，时长 MUST ≤ 400ms（§8.4 桥接片段上限）。

#### Scenario: 预设决定转场

- **WHEN** 页规格 `presetId=tech-dynamic`
- **THEN** 非首页 `transition.type=slide`，`durationMs` 取自预设并 ≤ 400

### Requirement: IR 是幂等的规范化字节

`planHash` MUST 为规范化 JSON 的 SHA-256，计算 MUST 覆盖 `schemaVersion`、画布、fps、字体版本、每页时长与全部运动数值；MUST NOT 覆盖任何时间戳、路径前缀、job id。

#### Scenario: 两次编译同一规格

- **WHEN** 同一 spec、同一几何、同一时间轴编译两次
- **THEN** 两次 `planHash` 相同，字节级一致（不含 `warnings` 以外的易变字段）

#### Scenario: 改一个像素就换 hash

- **WHEN** 仅把某元素盒宽改 1px
- **THEN** `planHash` 改变

### Requirement: 渲染服务无状态且幂等

服务 MUST 提供 `POST /v1/render`（返回 `jobId`）、`GET /v1/render/{jobId}`、`DELETE /v1/render/{jobId}`、`GET /healthz`。相同 `planHash` 的重复提交 MUST 返回同一 `jobId` 且不重复渲染，**唯一例外是已有 job 落在 `failed` 或 `canceled` 终态**。

失败与取消表示「这次没成」，MUST NOT 让该 `planHash` 永久不可渲染：重试 MUST 重新渲染，并 MUST 分配新的 `jobId`（重试需要独立的工作目录，不能与被取消那一轮残留的清理抢同一路径）。旧 `jobId` MUST 仍可查询，并保持其终态与错误信息不变。

#### Scenario: 重复提交

- **WHEN** 同一 plan 连续提交两次
- **THEN** 两次返回同一 `jobId`，backend 只被调用一次

#### Scenario: 失败后重试

- **WHEN** 某份计划渲染失败（瞬时故障），调用方用完全相同的 plan 再次提交
- **THEN** 服务 MUST 重新渲染而不是复用失败的 job，返回一个新的 `jobId`；旧 `jobId` 查到的仍是 `failed`

#### Scenario: 取消后重试

- **WHEN** 任务被 `DELETE` 取消后，调用方用完全相同的 plan 再次提交
- **THEN** 服务 MUST 重新渲染并返回新的 `jobId`，产物 MUST 落在新目录

#### Scenario: 已成功后重复提交

- **WHEN** 任务已 `succeeded`，调用方再次提交同一 plan
- **THEN** 返回同一 `jobId`，MUST NOT 重新渲染（幂等只服务成功结果）

#### Scenario: 非法 plan

- **WHEN** plan 未通过 IR 校验
- **THEN** 返回 400 与具体字段路径，MUST NOT 创建 job

### Requirement: 安全边界可验证

服务 MUST 拒绝工作根目录之外的 `baseImage` 路径（含 `..`、绝对路径、盘符）。MUST 限制请求体大小与页数/帧数上限。

#### Scenario: 路径逃逸

- **WHEN** `baseImage` 为 `../../etc/passwd` 或 `C:\Windows\win.ini`
- **THEN** 返回 400，错误码 `unsafe_path`

### Requirement: 客户端区分可重试与永久失败

客户端 MUST 把 4xx（除 408/429）归为永久失败并返回带字段路径的错误；5xx/408/429/超时 MUST 按指数退避重试 2 次。服务不可达或 `/healthz` 失败 MUST 返回 `ErrAnimationUnavailable`，供上层降级到静态导出。

#### Scenario: 服务未部署

- **WHEN** 渲染服务地址不可达
- **THEN** 客户端返回 `ErrAnimationUnavailable`，上层据此回退静态路径且不向用户报错

#### Scenario: 校验失败不重试

- **WHEN** 服务返回 400
- **THEN** 客户端不重试，直接返回错误
