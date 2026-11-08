# 任务

## 1. 存储层：把「多页写」收成一个事务（Go）

- [x] 1.1 `internal/animation/animstore/store.go`：新增 `BatchOp{Key, Rev, Expected}` 与
      `ApplyBatch(ctx, ops) ([]*animation.Revision, error)`。`Rev=nil` 表示删除该页规格，
      用于撤销「套用前没有规格」的页——不写空草稿是硬要求
- [x] 1.2 `store_sqlite.go`：抽出 `sqPutInTx` / `sqDeleteInTx`，`Put` 改为「只含一个 op 的事务」；
      `ApplyBatch` 用 `db.BeginTx`，任一 op 失败 `tx.Rollback()`，错误带 op 序号与 slideID
- [x] 1.3 `store_postgres.go`：`pgPutInTx` / `pgDeleteInTx` / `ApplyBatch`，
      事务包在 `tenant.Run` 内，保留 RLS 租户隔离，拒绝跨租户写
- [x] 1.4 `batch_test.go`：`TestSQLiteApplyBatchWritesAllOrNothing` /
      `...RollsBackOnMidBatchConflict` / `...DeleteRestoresAbsentPage`

## 2. API 层：预检零写入 + 服务端快照（Go）

- [x] 2.1 `internal/api/animation_applybatch.go`：实现 `applyBatch` / `restoreBatch` /
      `batchGuard` / `snapshotOf` / `cloneTracks` / `batchBindings`；请求体 1 MiB、单次 ≤200 页、
      `DisallowUnknownFields`、源页与目标页都按真实 deck 页序校验
- [x] 2.2 预检覆盖：`locked`（`overwrite` 也不能越过）、`approved` / 人工微调（默认拒绝）、
      页面不可用、页时长未知、规格生成失败 / `rev.Validate(pc)`；
      任一不合格整单 `422 batch_ineligible` + 逐页原因
- [x] 2.3 **真机验证后修正语义**：源页只贡献 `PresetID` / `Intensity`，每页走
      `batchBindings` + `defaultSpecFor` 按自身元素与讲稿重算轨道；
      `presetId` 为空的自定义规格整单拒绝 `source_missing`；不带走 `tracks` / `Shots`
- [x] 2.4 整单被拒时把合格的页从 `applied` 改写成 `skipped` + `batch_rejected`，
      零写入的响应里不许出现「已套用」
- [x] 2.5 `restoreBatch`：快照重新过 V1–V12，`existed=false` 走 `Rev=nil` 删行，
      脏快照整单拒绝零写入
- [x] 2.6 `internal/api/animation.go` 注册两条路由；`web/route-inventory.json` 的
      `animation-spec` 分组登记两条前缀并补齐 reason（`npm run build` 的 `route:check` 由此通过）
- [x] 2.7 `animation_applybatch_test.go` 7 条：套用成功 / 不合格零写入（含响应不得自称 applied）/
      locked 永不越过 / 缺失与未知页 / 整批撤销 / 脏快照不写入 /
      **不同页不重合元素 ID 时仍成功且轨道锚定本页**（`TestApplyBatchRegeneratesPerPageAnchors`）

## 3. 前端：多选、一次撤销、诚实反馈

- [x] 3.1 `web/src/animationBatch.ts`：`batchTargets` / `batchApplyConsistent` /
      `summarizeBatchReasons` / `restorableSnapshots` 四个纯函数
- [x] 3.2 `web/src/api/animation.ts` 与 `index.ts`：`applyAnimationBatch` / `restoreAnimationBatch`
      及响应类型；逐页 `status` 用 `'applied' | 'skipped'` 字面量联合（宽泛 `string` 过不了 tsc）
- [x] 3.3 `web/src/pages/ProjectEditor.tsx`：多选态、勾选、套用/撤销、成功响应复核、
      `batch_ineligible` 逐页原因；多选态只影响页轨，模板固定为进入多选时的当前页
- [x] 3.4 `web/src/styles.css`：rail-title flex 布局与批量面板/按钮/选中描边
- [x] 3.5 `web/src/animationPreview.ts` 补 `validation_failed` / `spec_failed` /
      `batch_rejected` / `invalid_snapshot` 原因文案；`zh.json` / `en.json` 共 16 条批量套用文案；
      按钮与说明文案如实写明「套的是风格、按每页内容重算、不复制元素级微调」
- [x] 3.6 修掉本轮引入的 `t('common.error')`：该键在 i18n 里不存在，
      `i18n:check` 只告警不阻塞，界面会原样显示 key；改用
      `editor.animationBatchFailed` / `editor.animationBatchInconsistent`

## 4. 门禁

- [x] 4.1 `web/animation-batch-check.mjs`（7 项）挂进 `npm run build`：页集合、
      响应自洽、原因汇总、快照自检、UI 静态检查、i18n 键对等且不得引用未登记键、后端路由注册
- [x] 4.2 `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿
- [x] 4.3 `npm run build`（contrast / api:check / route:check / 全部静态与单测门禁 /
      tsc -b / vite build）、`npm run build:share`、`npm run i18n:check`（中英键对等）、
      `git diff --check` 全绿

## 5. 文档与部署

- [x] 5.1 本变更 proposal / design / tasks / spec delta；`openspec validate` 通过并
      `openspec archive` 合并进 `openspec/specs/animation-spec/spec.md`（+3 Requirement）
- [x] 5.2 交叉编译 Linux 二进制，部署 8077 预览实例与 8080 主实例（均先备份旧二进制），
      两个单元组 active、`/healthz` 200
- [x] 5.3 **真实数据端到端验证**：8077 上 23 页 deck / 11 份既有规格，
      验证路由存活、`batch_ineligible` 零写入（且响应不自称 applied）、3 页批量套用
      （风格一致、轨道锚点全属于本页元素、非源页副本）、一次撤销回到请求前状态；
      8080 是 Postgres 生产 profile、未显式开 `PPTS_ANIMATION_ENABLED`，部署闸按设计关闭，
      用「能力闸 JSON 404」与「路由器纯文本 404」对照证明两条路由已注册。
      验证脚本留在 `tmp/verify_batch.py`（`tmp/` 已 gitignore，不入库）。
- [x] 5.4 一次 commit + push；追加工作日志
