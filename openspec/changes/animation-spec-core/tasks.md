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

## 4. 存储与迁移（PR2）

- [ ] 4.1 `store.go` 定义 `Store` 接口（`Get`/`Put`/`SetStatus`）与 `Key`
- [ ] 4.2 `migrations/` 新增 `animation_revisions`：主键 `id`、唯一键 `(tenant_id, project_id, source_revision_no, slide_id, language)`、列 `revision`/`status`/`tracks jsonb`/`preset_id`/`intensity`/`rendered_revision`/`updated_at`、RLS 策略
- [ ] 4.3 `store_sqlite.go` 实现（`tracks` 以 TEXT 存 JSON，行为与 pg 对齐）
- [ ] 4.4 `store_postgres.go` 实现，写路径 `UPDATE ... WHERE revision = $expected`，`RowsAffected == 0` → `ErrConflict`
- [ ] 4.5 共享 `storetest` 表驱动用例：并发写、锁定拒写、legacy 回退、唯一键、跨租户隔离（含 RLS 生效验证）
- [ ] 4.6 迁移在空库与既有库上各跑一次（`migrate up` / `down`），确认可回滚

## 5. API 接线（PR2）

- [ ] 5.1 `internal/api/animation.go`：`GET` 单页规格（`?default=true` 生成不落库）与 `PUT` 单页规格（带 `expected_revision`）
- [ ] 5.2 写路径受 `animation.enabled` 开关控制，关闭时返回 404；读路径不依赖该开关
- [ ] 5.3 错误映射：`ErrValidation`→400、`ErrLocked`/`ErrConflict`→409、超限→413、`ErrNotFound`→404
- [ ] 5.4 handler 测试：合法读写、409 冲突带回最新版、锁定 409、开关关闭 404、租户隔离 404

## 6. 验收门禁（PR1 部分已达成）

- [x] 6.1 `go build ./... && go vet ./internal/animation/...` 通过（2026-10-28 实跑）
- [ ] 6.2 `go test ./internal/animation/... -race` — 用例全绿，但本机无 gcc，`-race` 需 `CGO_ENABLED=1`，留 CI 复跑
- [x] 6.3 `go list -deps ./internal/animation` 不含 `internal/media`、`internal/app`、`internal/api`（已实跑，仅依赖 `internal/project` 与标准库）
- [x] 6.4 `go.mod` / `go.sum` 零改动（`git status` 可验），无新增依赖
- [x] 6.5 `internal/app/export.go`、`internal/media/`、`web/` 零改动（`git diff --stat` 为空）
- [x] 6.6 非法输入拦截：23 条校验用例覆盖 V0–V12，全部返回预期规则编号与状态码；HTTP 层用例属 PR2
- [ ] 6.7 回写方案文档 M1 实际落地与偏差（见 design.md「实施记录」），并同步 `openspec/specs/`（归档时生成）
