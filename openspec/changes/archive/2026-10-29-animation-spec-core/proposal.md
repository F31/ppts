# Proposal

## Why

现有导出链路只能把「整页 PNG + 页时长」交给 `MP4Encoder`，页内没有任何元素身份与时间函数，因此做不出「第 3 条要点在讲稿第 4 段开始时才出现」这类效果。设计侧结论（《动画视频扩展-设计方案 V1.0》§1.1、§2.2）是：动画不是重构，而是一条与静态讲解并列的新通道，其事实源必须是一等可编辑数据 `AnimationSpec`。

先把「规格正确」这件事锁死，再谈渲染：动画规格一旦在前端或渲染器里被隐式解释，后续所有版本、并发、缓存与降级语义都会跟着漂移。本变更只做 L1 规格层，纯 Go、无外部依赖、不接线、不改变任何现有导出行为，可独立合并与回滚。

## What Changes

- 新增 `internal/animation` 包：`Revision → Track → Clip` 三层模型、`Status` 状态机、`Params` 参数集，逐字段对齐 `internal/narration` 的源版本隔离与乐观并发语义。
- 新增动效白名单 `catalog`：以单一 JSON 源定义动效 id、适用元素类型、参数取值范围与时长区间，Go 侧生成强类型表；未登记动效一律拒绝。
- 新增服务端权威校验 V1–V12（白名单、元素存在性、时间边界、重叠、强度与时长区间、页时长预算、`withSegment` 锚点有效性、`locked` 只读、`expected_revision` 冲突、数量上限、字节上限）。
- 新增默认规格生成：给定页的 `project.Page`（含 `Shape.Bounds`）与预设 id，产出一份可直接落库的默认 `Revision`，使「零配置出片」在规格层即可成立。
- 新增 `Store` 接口与 sqlite/postgres 双实现（含迁移），并发写用 `UPDATE ... WHERE revision = $expected`，影响行数为 0 即 `ErrConflict`。
- 新增 HTTP 读写契约（`GET`/`PUT` 单页规格），写接口默认由开关控制，读接口可先上线。
- **不包含**：渲染器、`compile.go`、`export.go` 分支、前端编辑器。这些属于后续变更（M2/M3），本变更不引用 `media`/`app`/`api` 的运行时行为。

## Capabilities

### New Capabilities

- `animation-spec`: 单页动画规格的定义、默认生成、白名单校验、版本与并发语义、持久化契约。它拥有「什么是一份合法动画规格」这一条长期行为，后续渲染与编辑都消费它。

### Modified Capabilities

（无。现有静态导出、讲稿、时间轴的行为均不变。）

## Impact

**新增代码**

- `internal/animation/`：`model.go`、`catalog.go` + `catalog.json`、`validate.go`、`defaults.go`、`store.go`、`store_sqlite.go`、`store_postgres.go`
- `migrations/`：`animation_revisions` 建表 + 唯一索引 + RLS
- `internal/api/`：单页规格读写 handler 与路由（写路径受 `animation.enabled` 控制）

**依赖方向约束**（验收项，非建议）

- `go list -deps ./internal/animation` MUST NOT 包含 `internal/media`、`internal/app`、`internal/api`
- 不引入任何新第三方依赖；catalog 用标准库 `encoding/json` 读取
- 主 API 镜像不因本变更增加运行时依赖

**受影响但行为不变的既有系统**

- 导出管线、讲稿、时间轴：零改动。本变更不触碰 `internal/app/export.go`。
- 数据库：仅新增单表，无外键到 `slides`；`slide_id` 仅文件内唯一，靠 `source_revision_no` 区分版本。

**风险**

- 并发与冲突语义若与 `internal/narration` 不一致，前端会拿到两套不同的冲突处理方式 → 用同一套 Store 测试用例（同表驱动）交叉验证。
- `withSegment` 锚定的有效性依赖讲稿修订，跨包只读引用需明确不产生循环依赖。
