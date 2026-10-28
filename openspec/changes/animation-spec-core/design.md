# Design

## Context

仓库现状（已核实）：

- `internal/media/mp4.go` 的 `MP4EncodeOptions` 只接受 `PagePNGs [][]byte` + `PageDurationsMS []int64` + 音频 + 字幕，帧源粒度是整页位图，页内无元素身份。
- `internal/project/model.go` 的 `Page.HasTiming` 只能标记「含 `p:timing`」，`Shape{ID, Kind, Bounds *Box, Children, ...}` 已提供元素 id 与 EMU 包围盒，无需改解析器即可作为动画锚点。
- `internal/narration` 已验证一整套隔离与并发语义：`Revision{SlideID, Language, SourceRevisionNo, Revision, Status, Segments}`、`expected` 版本写、`ErrConflict`、`ErrLocked`、源版本 0 = legacy。
- `internal/pipeline` 提供 job/step/lease 语义，本变更不涉及。

约束：本变更只做 L1，不得让动画包反向依赖 `media`/`app`/`api`；不得引入新第三方依赖；静态导出与讲稿行为零变化。

## Goals / Non-Goals

**Goals:**

- 定义一份可长期稳定、可审计、可复现的动画规格，成为编辑与渲染的共同事实源。
- 把「什么算合法」固化在服务端，前端只做即时提示，不承担权威裁决。
- 与讲稿复用同一套源版本隔离、乐观并发、审批锁定语义，避免两套冲突处理方式。
- 纯 Go 可测：无渲染器、无 Chromium、无 Node，`go test ./internal/animation/...` 即可全绿。
- 零配置可用：未配置页也能通过默认生成得到一份合法规格。

**Non-Goals:**

- 渲染、编译（`Spec + Timeline → RenderPlan`）、导出接线（后续 M2/M3 变更）。
- 前端编辑器与轨道拖拽。
- 矢量重绘（L3）、源 PPT 自带动画还原。
- 增量重渲与缓存键设计（M5）。

## Decisions

### D1 单表 + JSONB 存 `tracks`，不拆表

单页轨道通常 3–15 条，查询永远是「按页取整份」，没有按 clip 聚合的需求。拆表会带来额外事务、迁移与 N+1 风险，与「轻量」原则冲突。十万级页面下，`(tenant_id, project_id, source_revision_no, slide_id, language)` 唯一索引足以支撑读路径。

备选：`tracks`/`clips` 两张关系表——被否决，收益仅在于单 clip 级查询，而该场景不存在。

### D2 catalog 用单一 JSON 源，Go 侧生成强类型表

`catalog.json` 是唯一事实源；构建期生成 Go 常量表与校验所需的参数约束。渲染器侧（M2）将消费同一份 JSON，避免 Go/TS 两端漂移。新增动效只改 catalog + 一个渲染组件，核心校验链路零改动。

备选：Go 结构体硬编码——被否决，会导致 M2 出现第二份定义。

### D3 校验顺序固定：鉴权 → 锁定 → 并发 → 结构 → 业务规则

顺序是有语义的，不能随意：

1. 租户上下文与权限（否则可能泄露他人数据的存在性）
2. `status == locked` → 409 `ErrLocked`
3. `expected_revision` 不匹配 → 409 `ErrConflict{Latest}`
4. 结构与类型（JSON schema、必填字段）→ 400
5. 业务规则 V2–V8、V11–V12 → 400/409/413

理由：锁定与并发是「你无法写」的问题，结构错误是「你写错了」的问题。先判前者，用户得到的提示才与真实原因一致；否则会出现在只读页面上被反复纠正字段格式的荒谬体验。

### D4 `withSegment` 在写路径保持相对引用，只在编译期解析

配音重生成后各段时长会变，若写路径把 `withSegment` 固化成绝对毫秒，讲稿一改规格就错。因此：

- 写路径只校验「该 segment 在当前讲稿修订中存在」，原样存储 `withSegment:seg-2`。
- 绝对时间换算属于 `compile.go`（M2 变更），本变更不实现。
- 讲稿删除分段时，读路径返回「锚点失效」的标记字段，前端提示并允许降级为 `onEnter`（前端 M3 变更）。

这意味着 `internal/animation` 需要一个**只读**的讲稿分段查询接口。为避免包级循环依赖，接口定义在 `animation` 侧（消费者定义接口），由 `app` 层注入 `narration` 实现。

### D5 Store 接口与 narration 同构，sqlite/postgres 共享测试

```go
type Store interface {
    Get(ctx context.Context, key Key) (*Revision, error)          // 未命中 → ErrNotFound
    Put(ctx context.Context, rev *Revision, expected int64) (*Revision, error)
    SetStatus(ctx context.Context, key Key, status Status) error
}
```

- `Put` 用 `UPDATE ... WHERE revision = $expected`，`RowsAffected == 0` 且记录存在 → `ErrConflict`。
- 两个实现跑同一套 `storetest` 表驱动用例（含并发写、锁定、legacy 回退），避免「pg 对了 sqlite 错了」。

### D6 校验不依赖渲染参数

分辨率、fps、字体、像素换算全部不在本层出现。`start_ms`/`duration_ms`/`offset` 一律以 1080p 基准的逻辑量表达，换算在编译期按 `renderProfile` 完成。这样同一份规格可同时服务 16:9 正片与 480p 预览。

### D7 默认规格生成是纯函数

`DefaultSpec(page project.Page, presetID string) *Revision`：输入页与预设，输出确定性规格。规则：标题走 `slideIn`（`onEnter`）、要点逐条 `fadeIn`（`withSegment`，若讲稿分段可用则按序绑定，否则 `onEnter` + 递增 `start_ms`）、图片走轻微 `pop`。无几何信息的元素跳过。

确定性要求是为了让「首次打开就能出片」和快照测试成立：同输入必须同输出。

### D8 API 契约：读先上，写受开关保护

- `GET /api/projects/{projectId}/slides/{slideId}/animation?sourceRevisionNo=&language=&default=true` → 规格；未保存且 `default=true` 时按预设生成一份（不落库）
- `PUT /api/projects/{projectId}/slides/{slideId}/animation` → 带 `expectedRevision`
- 写路径受开关控制；开关关闭时读接口仍可用（便于灰度前验证契约），写接口返回 404（不暴露能力存在）

**为什么前缀是 `/api/` 而不是设计方案写的 `/v1/`**：仓库里唯一的 REST 先例是 `/api/context-rules`（`internal/api/contextrule.go`），业务 RPC 面全部走 ConnectRPC。新增 `/v1/` 会变成第三种前缀；跟随 `/api/` 只多一套"资源式 CRUD vs RPC"的分工，不引入第三套命名。这也是实施时才暴露的设计缺口——方案写的是抽象契约，仓库有它自己的既有约定。

### D9 错误用哨兵 + 结构化明细

`ErrConflict`/`ErrLocked`/`ErrNotFound` 为哨兵错误；校验失败返回 `ValidationError{Rule: "V3", Field: "tracks[1].clips[0].startMs", Message: "...", Hint: "..."}`。规则编号直接对应设计方案 §5.4，便于前端定向提示与埋点统计（`animation_spec_validation_failed_total{rule}`）。

## Risks / Trade-offs

| 风险 | 缓解 |
|---|---|
| 与 `narration` 语义漂移（冲突/锁定表现不一致） | Store 层复用同构测试用例；错误码与前端提示文案对齐 |
| 校验规则与编辑器文案不同步 | 规则编号（V1–V12）作为唯一引用键，前端提示按编号映射，不复制文案 |
| `withSegment` 校验需要读讲稿，可能引入包级耦合 | 消费者侧定义窄接口（只读 `SegmentsBySlide`），由 `app` 注入实现 |
| 默认规格「看起来能用但不好看」 | 默认值必须来自 catalog 与预设定版，M2 出片后再标定具体数值；本变更只保证合法性 |
| JSONB 存储导致后续按 clip 查询困难 | 明确记录该决策的重新评估条件：出现「跨页统计动效使用率」的运营需求时再考虑物化视图 |

## 实施记录（PR1，2026-10-28）

落地时发现三处设计方案内部不一致或需补充的地方，处理方式如下：

1. **`bob` 时长区间与 V5 冲突。** 附录 D 写 `bob.durationRange = [1000, 4000]`，而 §5.4 V5 要求 `DurationMs ∈ [120, 3000]`，照抄会让合法的 `bob` 被 V5 拒绝。处理：catalog 收敛为 `[1000, 3000]`——校验规则是产品承诺，catalog 是它的参数，不能反过来放宽规则。
2. **`slideIn.appliesTo` 不含 `shape`，但 `tech-dynamic` 需要形状位移。** 处理：该预设的 `shape` 改用 `fadeIn`，不扩展 `appliesTo`——色块/容器做位移容易与背景撕裂，视觉上不划算。
3. **V6 在 V3 成立时是冗余保护。** 每条 clip 的 `end` 都不超过「页时长 + 1 帧」，并集不可能超限，因此 V6 不会先于 V3 触发。保留为防御（未来若放宽 V3 或引入跨页桥接片段，它才成为唯一护栏），并用 `unionLength` 独立单测固定语义。

两处实现取向调整（不改变对外契约）：

- catalog 不用 codegen，改为 `//go:embed` + 启动解析 + 自检：少一个生成步骤，也就少一种「忘了重新生成」的漂移。
- `withSegment` 合法性由 `PageContext.SegmentIDs` 注入，而非直接依赖 `internal/narration`：L1 依赖面收敛为「标准库 + internal/project」，比消费者侧窄接口更简单，也与 §4.2 的依赖方向一致。

## 实施记录（PR2，2026-10-28）

**存储层不做 legacy 分叉。** 讲稿在旧源版本上首次编辑会从 legacy 复制一份，因为文字与版本弱相关；动画规格强依赖元素 id 与页时长，旧版锚点在新版排版里可能已经不存在，静默继承会直接产出校验失败的数据，用户看到的却是"一打开就报错"。所以新源版本首次写入即新建，由调用方用 `DefaultSpec` 生成——**读**仍回退 legacy（存量可见），**写**不继承（避免脏锚点）。

**新增 `ErrStatusTransition`。** 状态机只允许 `approved ← draft|locked`、`locked ← approved`；`draft → locked` 跨级被拒。理由：锁定意味着"已审阅并定稿"，允许越过审阅直接锁定会让审批记录失去意义。锁定状态下的写入一律 `ErrLocked`，且**先于**版本冲突判断——"不能写"比"你的版本旧了"更根本，否则用户会去刷新重试，还是写不进去。

**页时长在编辑期可能未知。** 时间轴要等配音完成才有，读取入口属于 M2 的编译期。因此 `PageContext.DurationMS = 0` 被定义为"未知"，此时跳过 V3 上界与 V6 预算，只做结构与下界校验，响应带 `needsDurationRecheck: true`。若不这样做，"先编排动画、后生成配音"这个正常顺序会被直接堵死；渲染前必须用真实时长再校验一次，这个兜底不能省。

**传输形态与设计方案冲突（已决）。** §5 给的是 REST（`GET/PUT /v1/projects/{id}/slides/{slideId}/animation`），而本仓库业务接口全部为 ConnectRPC，`net/http` 路由只承载 health/metrics/auth/object。按 REST 落地会成为第二套约定，按 Connect 落地要改 `proto/ppts/v1` 并重新生成 `gen/`。已决策：沿用仓库既有 REST 先例（`/api/` 前缀 + `auth` 中间件），不引入 proto 生成链路（本机没有 buf 与 protoc 插件，且生成物无法验证）。业务 RPC 面维持 Connect 不变。
