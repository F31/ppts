# 设计：批量套用动效

## 1. 两段式协议

`applyBatch` 不做「边检查边写」，因为写了一半再失败需要补偿，而补偿本身又会失败。
协议固定为：

```
① 预检（只读）
   解析 deck 页序 → 校验 sourceSlideId / slideIds 都在当前 sourceRevisionNo
   读源页规格（必须已保存、必须有 presetId）→ 取 presetId / intensity
   对每页：store.Get → batchGuard → pages.Resolve → 页时长
           → batchBindings → defaultSpecFor（按本页元素重算轨道）→ rev.Validate(pc)
   任一页不合格 ⇒ 422 batch_ineligible + 逐页 reasons，**零写入**
② 提交（单事务）
   全部合格 ⇒ animstore.ApplyBatch(ops) ⇒ 任一 op 失败 tx.Rollback()
   成功 ⇒ 200 { applied, results, previous }
③ 撤销（单事务）
   restoreBatch(previous) ⇒ 同一 ApplyBatch，Existed=false 的页 Rev=nil 删行
```

`batchGuard` 的判定顺序是刻意的：**先回答「能不能写」，再回答「写错了什么」**，
与 `Revision.CheckWritable` / 设计方案 §5.4 同一口径。
`locked` 在最前面，`overwrite` 不能越过它。

## 2. 存储层：`ApplyBatch` 是唯一的多页写路径

```go
type BatchOp struct {
    Key      animation.Key
    Rev      *animation.Revision // nil = 删除这一页的规格
    Expected int64
}
ApplyBatch(ctx context.Context, ops []BatchOp) ([]*animation.Revision, error)
```

- SQLite：`db.BeginTx` → 逐个 `sqPutInTx` / `sqDeleteInTx` → `tx.Commit()`；
  任一错误 `tx.Rollback()`，错误带 `batch op i (slideID)` 定位。
- PostgreSQL：`tenant.Run(ctx, pool, tenantID, ...)` 包住事务，保留 RLS 租户隔离，
  拒绝跨租户写。`pgPutInTx` / `pgDeleteInTx` 与单页 `Put` 共用，避免两套校验漂移。
- `Put` 被改写成「开一个只含一个 op 的事务」，保证单页与批量走同一段代码。

`Expected` 是乐观并发版本：预检时读到的 `prev.Revision`（无规格时为 0）。
窗口内被别人改过就 `ErrConflict` → 整批回滚。

## 3. 为什么是「套风格」而不是「复制轨道」

**真机验证先否掉了第一版实现**。第一版把源页 `Tracks` 深拷贝后原样写到目标页。
Go 单测 9/9、前端门禁 7/7、全量构建全绿；部署到 8077 用真实 22 页配音 deck
（`cf991cc7…`）一跑，3 个目标页**全部** `validation_failed`、零写入：

```text
HTTP 422 batch_ineligible
results: slide-260 validation_failed / slide-301 validation_failed / slide-300 validation_failed
```

根因是 V2「元素锚点必须存在于当前页」：`Track.ElementID` 是页内形状 ID，
`Clip.trigger` 的 `withSegment:<id>` 绑的也是本页分段 ID。真实 PPT 里不同页的形状 ID
本来就不重合，跨页复制在语义上没有定义。单测全绿只是因为 `applyAllTestResolver`
给每页复用了同一批 `shape-1/2`。**不放宽校验来"修"它**——那只会存下一堆指向不存在元素的
假规格，接口 200、成片没有动效。

修正后的生成链与 `applyOneSlide` / 单页 `?default=true` 逐字共用：

```go
dto, _, err := h.defaultSpecFor(page, pc, key, source.PresetID, batchBindings(page, pc))
rev := &animation.Revision{
    ..., PresetID: source.PresetID, Intensity: source.Intensity, Tracks: dto.Tracks,
}
if err := rev.Validate(pc); err != nil { /* spec_failed / validation_failed，整单拒绝 */ }
```

- 源页只贡献 `PresetID` 与 `Intensity`。`Intensity` 在 `animdefault` 里只是元数据、
  不参与 clip 时间计算（见 `defaults.go`），因此用源页值覆盖是安全的。
- `PresetID` 为空（自定义规格）时返回 `422 source_missing`，不静默退回默认预设。
- `Shots` 是源页 EMU 裁切框，同样不带走；生成链本来也不产出 `Shots`。
- `pc.Segments` 非空时 `batchBindings` 走 `bind.Run` 预填，与一键套用同口径；
  一条都没绑上时 `animdefault` 诚实产出空轨道，不另猜兜底编排。

回归盲区由 `TestApplyBatchRegeneratesPerPageAnchors` 钉住：新夹具
`distinctAnchorResolver` 给每页分配不重合的形状 ID 与分段 ID，断言批量套用成功、
且目标页轨道只锚定本页元素。把实现改回「复制源页 tracks」，这条会立刻红。

`cloneTracks` 仍保留，但只用于 `previous` 快照与 `restore`：快照/恢复的每页必须持有
独立的 `*Clip` 指针，否则共享指针会让一处改动串到另一页。

## 4. 成功响应必须自洽（前端）

后端 `applied` 是一个汇总数字；如果它和 `results` 里 `status=applied` 的条数、
或 `previous` 快照数对不上，界面就不该说「已套用」：

```ts
export function batchApplyConsistent(resp: BatchApplyResponse | null | undefined): boolean {
  if (!resp || !Array.isArray(resp.results) || !Array.isArray(resp.previous)) return false;
  const applied = resp.results.filter((item) => item?.status === 'applied').length;
  if (typeof resp.applied === 'number' && resp.applied !== applied) return false;
  if (resp.applied === 0) return false;
  return resp.previous.length === applied;
}
```

`restorableSnapshots` 另做一次校验（非空 / 页 ID 不重复 / 不为空串），
通过才允许「撤销上次套用」按钮可用。`422 batch_ineligible` 走单独分支：
把逐页原因用 `summarizeBatchReasons` 聚成一句可行动的复核清单，而不是笼统报「操作失败」。

**整单被拒时响应也不能说谎**。预检循环里合格的页原本会被标成 `applied`，
但整单 422 意味着一页都没写；原样返回等于告诉用户「这几页套好了」。
因此拒绝分支把所有 `applied` 改写成 `skipped` + `batch_rejected`
（「因其它页不合格而未写入」），由 `TestApplyBatchIneligiblePageWritesNothing` 钉住。

## 5. 前端交互边界

- 多选态只影响**左侧页轨**：点页 = 勾选/取消，不再切页。模板固定为进入多选时的当前页；
  想换模板必须先退出多选，避免「模板悄悄换了」。
- 退出多选清空勾选，但**保留 undo 快照**——用户可能先退出多选再决定撤销。
- `sourceRevisionNo` / `language` 与单页编辑器共用同一份 `batchScope`，
  避免批量写入落到另一个源版本。

## 6. 文案与门禁

- 本变更共 16 条中英批量套用文案（12 条交互 + 2 条兜底 + `spec_failed` +
  `batch_rejected` 两条原因码），另把 `animationBatchNote` / `animationBatchApply` 的文案
  改成如实说明「套的是风格、按每页内容重算、不复制元素级微调」。
  过程中发现本轮引入的 `t('common.error')` 在 i18n 里**根本不存在**，
  而 `i18n:check` 对「源码引用了不存在的 key」只告警不阻塞——界面会原样显示 key。
  已改为 `editor.animationBatchFailed` / `editor.animationBatchInconsistent`，
  并在 `animation-batch-check.mjs` 加一条静态断言：所有 `editor.animationBatch*` 键
  必须同时存在于 zh/en，且源码不得再引用 `common.error`。
- `web/animation-batch-check.mjs`（7 项）挂进 `npm run build`：
  页集合整理、响应自洽、原因汇总、快照自检、UI 静态检查、i18n 键、后端路由注册。
- `web/route-inventory.json` 的 `animation-spec` 分组登记两条新路由，
  reason 写清「源页只贡献风格、目标页按自身元素重算」与 `apply-all` 的语义差异
  （预检全合格 + 单事务 + previous 快照）。
- **界面文案不许暗示"复制了源页规格"**：按钮为「套用当前页风格」，说明文案明确
  「每页按自己的内容与讲稿生成规格，不复制源页的元素级微调」。

## 7. 与 §7.6 的偏离，以及为什么

§7.6 写「撤销/重做：本地栈（≥20 步），不落库」。这句话成立的前提是
**一次操作只改浏览器里的草稿**（单页轨道微调）。批量套用改的是 N 页数据库记录，
本地栈撤不回来。本变更因此采用「服务端快照 + `restore` 单事务」，
且只提供**一次撤销**。做多步/跨会话撤销需要服务端按操作 ID 保管快照，
那是另一个数据模型，不在本变更里含糊带过。

## 8. 已知限制

- 一次最多 200 页，请求体上限 1 MiB；超出返回 `too_many_slides` / `invalid_body`。
- 源页 `presetId` 为空（自定义规格）时整单拒绝 `source_missing`；自定义规格没有可套用的"风格"。
- 不复制元素级轨道与 `Shots`；要做「跨页复用一套编排」需要元素角色映射与分段重锚，另立变更。
- 快照无状态、由前端回传，服务端只校验内容合法性与页归属（见 proposal 风险）。
- 预检与提交之间的并发窗口靠 `Expected` 兜底，表现为整批 409，需要重选重试。
- `Approved` 页在 `overwrite=true` 时会被写入新规格；`animstore` 的 UPDATE 不改 `status` 列，
  因此状态仍是 `approved`。这是既有 `Put` 的语义，本变更不改它，但要在后续审批流变更里复核
  「换了内容却没重新审批」是否可接受。
