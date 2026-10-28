# Tasks

> 交付纪律：本变更对应设计方案 M1，纯 Go、无外部依赖、不接线。合并前必须通过 `go build ./...`、`go vet ./...`、`go test ./internal/animation/... ./internal/api/...`，且 `git diff` 不含 `internal/app/export.go`、`internal/media/`、`web/` 的任何改动。

## 1. 模型与 catalog（PR1 · 已完成）

- [x] 1.1 新建 `internal/animation/model.go`：`Revision`/`Track`/`Clip`/`Params`/`Status`/`Key`，字段与 `internal/narration.Revision` 对齐（含 `SourceRevisionNo` 的 legacy=0 语义注释）
- [x] 1.2 定义哨兵错误：`ErrNotFound`、`ErrConflict{Latest}`、`ErrLocked`、`ValidationError{Rule, Field, Message, Hint, Status}`
- [x] 1.3 新建 `internal/animation/catalog.json`：7 种动效（`fadeIn`/`slideIn`/`pop`/`grow`/`highlight`/`bob`/`fadeOut`，取自设计方案附录 D）与 4 套预设（`business-calm`/`tech-dynamic`/`teaching-clear`/`launch-bold`）；每项声明 `level`、`class`、`appliesTo`、`params` 区间、`durationRange`、`defaultDuration`（`class` 为 V4 所需，附录 D 未列，属必要补充）
- [x] 1.4 ~~用 `go generate` 生成强类型表~~ → 改为 `//go:embed catalog.json` + 启动时解析为强类型表，并在 `ParseCatalog` 内做全量自检（重复 id、非法 class、区间越界、预设引用不存在的动效/不适用元素）。不再需要生成步骤，也就不存在「忘了重新生成」的漂移
- [x] 1.5 单测：catalog 完整性（每种动效有默认时长、参数区间左 ≤ 右、预设引用的动效均存在、每套预设都有 `page` 兜底与 title/bullet/image 动效）

## 2. 服务端校验 V1–V12（PR1 · 已完成）

- [x] 2.1 `validate.go` 实现固定顺序：规模(V11) → 字节(V12) → 预设(V8) → 强度(V5) → 逐元素/逐 clip（V1/V2/V3/V4 聚合/V7）→ 时间预算(V6)；V9/V10 由 `CheckWritable` 先行判断
- [x] 2.2 实现 V1 白名单、V8 预设存在性（错误信息直接列出可选 id）
- [x] 2.3 实现 V2 元素锚点存在性，空 `elementId` 仅允许 `kind=page`，错误体列出可用元素 id
- [x] 2.4 实现 V3 时间边界与 V6 页时长预算（页时长由调用方注入，含 1 帧容差）
- [x] 2.5 实现 V4 同类动效重叠检测（入场/退场各 ≤1，强调可多条但同类型不得重叠），错误体给出冲突 clip 对
- [x] 2.6 实现 V5 `Intensity ∈ [0.5,1.5]`、`DurationMs ∈ [120,3000]`，并叠加 catalog 的 `durationRange`
- [x] 2.7 实现 V7 `withSegment` 锚点校验。**偏差**：不引入 narration 接口，改由 `PageContext.SegmentIDs` 注入合法分段 id——L1 由此完全不依赖讲稿包，耦合点收敛到编排层
- [x] 2.8 实现 V11 数量上限（轨道 ≤30、clip ≤40）与 V12 字节上限（≤256KB，返回 413）
- [x] 2.9 表驱动单测：23 条用例覆盖 12 条规则（含空轨道、非法缓动、非法 trigger、参数越界），断言规则编号与建议状态码

## 3. 默认规格生成（PR1 · 已完成）

- [x] 3.1 `defaults.go` 实现 `DefaultSpec(page, DefaultOptions)`：标题 `slideIn@onEnter`、要点 `fadeIn`（有分段则 `withSegment`，否则递增 `start_ms`）、图片 `pop`、图表 `grow`
- [x] 3.2 无 `Bounds` / `Opaque` / 音视频元素跳过且不阻塞整页；无可挂载元素或页太短时退化为 `kind=page` 的整页轨道
- [x] 3.3 单测：确定性（同输入 `reflect.DeepEqual`）＋ 4 套预设 × 3 种页面形态（纯文本/纯图片/纯表格）+ 综合页 全部通过 V1–V12
- [ ] 3.4 用真实样本 `智讲PPT-客户方案讲解.pptx` 解析出的页生成默认规格，作为 golden 用例入库（需先接 `internal/project` 的解析入口，留到 PR2 与 API 一起做）

## 4. 存储与迁移（PR2 · 已完成）

- [x] 4.1 `store.go` 定义 `Store` 接口（`Get`/`Put`/`SetStatus`）、`Key`、`ErrInvalidKey`、`ErrStatusTransition`
- [x] 4.2 迁移：`migrations/0051_animation_revisions.sql`（建表 + 唯一键 + RLS + 新表授权 + owner）与 `migrations/sqlite/0018_animation_revisions.sql`
- [x] 4.3 `store_sqlite.go`：tracks 存 TEXT，写入与版本判定同事务，`UPDATE ... WHERE revision = ?` 防插队
- [x] 4.4 `store_postgres.go`：`tenant.Run` 设 `app.tenant_id`，`tracks $n::jsonb`，`RowsAffected()==0` → `ErrConflict{Latest}`
- [x] 4.5 共享 `runStoreSuite`：插入/更新/过期冲突/插入时 expected≠0/锁定优先于冲突/状态机跨级/legacy 回退/精确版本优先/租户隔离/非法 key/nil 轨道落 `[]`；SQLite 实跑，PG 走 `-tags pg`
- [x] 4.6 迁移在空库与既有库上各跑一次并验回滚 — SQLite 由 `db.MigrateSQLite` 用例覆盖（空库）；
      PG 侧由 CI `postgres` job 每次执行：`postgres:17` 空库逐个应用全部迁移 → 再全量重放一遍
      （既有库口径），两步都 `ON_ERROR_STOP=1`（run 40785622034 全绿）。仓库没有 down 脚本，
      故「回滚」按「重放不炸既有库」的口径验证；重放曾真炸出 4 处问题（0029 函数改参数名、
      0037 缺 IF NOT EXISTS、0040 缺先 DROP、以及库名不是 `ppts_test` 导致函数授权分支不生效），
      均已修（commit 9914925 / 0965cf6）

## 5. API 接线（PR3 · 已完成）

> 传输形态已定：沿用仓库既有 REST 先例（`/api/` 前缀 + `auth` 中间件，与 `/api/context-rules` 同形），业务 RPC 面维持 Connect 不变。设计方案写的 `/v1/` 前缀不采用——它会成为仓库里的第三种前缀。
>
> 页时长在编辑期可能未知（时间轴要等配音完成）。`DurationMS = 0` 定义为"未知"：跳过 V3 上界与 V6 预算，响应带 `needsDurationRecheck`，渲染前必须用真实时长重校验（M2 编译期）。

- [x] 5.1 `internal/api/animation.go`：`GET`（`?default=true` 时按预设生成、不落库）与 `PUT`（带 `expectedRevision`）单页规格
- [x] 5.2 写路径受 `AnimationEnabled` 控制（本地 profile 默认开，生产读 `PPTS_ANIMATION_ENABLED`），关闭返回 404；读路径不受影响
- [x] 5.3 错误映射：`ValidationError`→按其 `Status`（400/409/413）、`ErrLocked`/`ErrConflict`→409（冲突带回 latest）、`ErrNotFound`→404、`ErrInvalidKey`→400
- [x] 5.4 handler 测试（sqlite + 假 resolver）：缺规格 404 / `default=true` 生成 / 读写往返 / 过期版本 409 带回 latest / V2 拒绝并给规则编号 / 缺 `expectedRevision` 400 / 开关关闭 404 / 锁定 409

## 6. 验收门禁（PR1 部分已达成）

- [x] 6.1 `go build ./... && go vet ./internal/animation/...` 通过（2026-10-28 实跑）
- [x] 6.2 `go test ./internal/animation/... -race` — **2026-10-29 CI 侧跑绿**（run `40780484914` 的 `race` job）。
      此前「本机没有 C 工具链」只解释了一半：真正的阻塞是 `lint` job 长期红着——`web/embed.go` 的
      `//go:embed all:dist` 需要 `web/dist` 里至少有一个文件，而 `.gitkeep` 被 vite 构建删掉后又被提交了删除，
      于是 `go vet ./...` 报 `pattern all:dist: no matching files found`，`race`/`test`/`corpus`/`postgres`/`s3`
      一律 skipped，连续十个 run 都是红的。修法见 commit `8c47e9d`（占位文件回仓库 + 构建后写回）。
      本机侧的可行路径也补进了 README「竞态检测（`-race`）」：WSL + go1.25.0 + gcc，
      `GOWORK=off go test -race -count=1 ./internal/render/... ./cmd/renderer/... ./internal/animation/...`
      全绿，`-count=3` 复跑仍绿（`-x` 确认编译参数含 `-race`，不是空跑）
- [x] 6.3 `go list -deps ./internal/animation` 不含 `internal/media`、`internal/app`、`internal/api`（已实跑，仅依赖 `internal/project` 与标准库）
- [x] 6.4 `go.mod` / `go.sum` 零改动（`git status` 可验），无新增依赖
- [x] 6.5 `internal/app/export.go`、`internal/media/`、`web/` 零改动（`git diff --stat` 为空）
- [x] 6.6 非法输入拦截：23 条领域用例覆盖 V0–V12；HTTP 层另有 8 条用例（含 V2 拒绝、409 冲突、锁定 409、开关 404）
- [x] 6.7 回写方案文档 M1 实际落地与偏差（见 design.md「实施记录」PR1/PR2/PR3）；`openspec/specs/` 由 archive 生成
- [ ] 6.8 归档为正式 spec（`openspec archive animation-spec-core`）：6.2（`-race`）与
      4.6（PG 迁移空库 + 重放）均已通，仅剩 3.4（真实样本 `智讲PPT-客户方案讲解.pptx` 的
      golden 用例）未做——它要接 `internal/project` 的解析入口，且样本 PPT 不在仓库里
