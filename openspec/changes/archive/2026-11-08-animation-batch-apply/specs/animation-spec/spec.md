# Spec Delta

## ADDED Requirements

### Requirement: 批量套用动效必须全成或全不成

系统 SHALL 支持以某一页已保存的动效风格（`presetId` 与 `intensity`）为模板，一次请求套用到多选的若干页。
该操作 MUST 采用「预检全部目标页 → 单事务提交」两段式：任何一页不满足条件时
MUST 返回带逐页原因的 `422 batch_ineligible` 且 MUST NOT 写入任何一页；
事务中任一页发生并发冲突时 MUST 整体回滚，MUST NOT 留下部分页已套用的中间态。
系统 MUST NOT 把「尽力而为、单页失败降级为 skipped」的 `apply-all` 语义用于本操作。

源页与目标页 MUST 都属于当前 `sourceRevisionNo` 的 deck；源页 MUST 已有保存的规格
且 MUST 带非空 `presetId`（自定义规格没有可套用的风格，MUST 整单拒绝，MUST NOT 静默退回默认预设）；
`locked` 状态的页 MUST 永不被越过（即使请求显式要求覆盖）。
系统 MUST 把「套用」实现为风格套用：每个目标页 MUST 用自身的元素与讲稿分段重新生成轨道，
并与「单页打开选同一预设」共用同一条生成链。系统 MUST NOT 把源页的 `tracks`
（其 `elementId` 与 `withSegment` 触发器都是页内标识）或 `Shots`（页内 EMU 裁切框）复制到目标页，
也 MUST NOT 通过放宽 V2 锚点校验来达成跨页复制。

#### Scenario: 一页不合格导致整单零写入

- **WHEN** 用户以第 3 页为模板勾选第 5、8、12 页，其中第 8 页处于 `locked` 状态
- **THEN** 系统返回 `422 batch_ineligible`，`results` 列出第 8 页原因为 `locked`
- **AND** 第 5、12 页的规格 MUST 与请求前逐字节一致，MUST NOT 产生新 revision

#### Scenario: 全部合格才提交

- **WHEN** 所有目标页都在 deck 内、未被锁定、页时长已知且按本页元素重新生成的规格通过 V1–V12
- **THEN** 系统 MUST 在一个事务内写入全部目标页，响应 `applied` 等于目标页数，
      且 `results` 中 `status=applied` 的条数与 `previous` 快照条数 MUST 都等于 `applied`

#### Scenario: 事务中途冲突整体回滚

- **WHEN** 预检通过后、提交时某一页的 `revision` 已被其它请求改过
- **THEN** 系统 MUST 返回 `409 batch_conflict` 并说明「整批已回滚」，
      MUST NOT 保留同批其它页的写入

#### Scenario: 跨页不复用源页轨道

- **WHEN** 源页轨道锚定 `shape-1`，而目标页只有 `shape-1-slide-2` 这类不重合的元素 ID
- **THEN** 批量套用 MUST 成功，目标页轨道 MUST 只锚定本页元素 ID，MUST NOT 出现源页的 `shape-1`
- **AND** 目标页写入的规格 MUST NOT 带源页的 `Shots`

### Requirement: 批量套用必须可用一次撤销

系统 SHALL 在批量套用成功响应中返回 `previous[]` 快照，快照 MUST 由服务端在写入前读取，
MUST NOT 由客户端自行推断「之前是什么」。系统 SHALL 提供 `apply-batch/restore` 端点，
按快照在单事务内恢复：套用前没有规格的页 MUST 删除该行（`Rev=nil`），
MUST NOT 写成一份空草稿。恢复内容 MUST 重新通过 V1–V12 校验，
快照页 MUST 属于当前 deck，任一快照不合法时 MUST 整单拒绝且零写入。

#### Scenario: 撤销还原有规格的页

- **WHEN** 用户撤销一次批量套用，某目标页套用前已有规格
- **THEN** 该页的 `presetId` / `intensity` / `tracks` MUST 恢复为快照内容，
      且 `status` 与其它未纳入快照的字段 MUST 遵循既有 `Put` 语义

#### Scenario: 撤销删除原本无规格的页

- **WHEN** 某目标页在套用前没有规格（快照 `existed=false`）
- **THEN** 撤销后该页 MUST 回到「从没配过」状态（记录不存在），
      MUST NOT 留下 `presetId` 为空的草稿行

#### Scenario: 脏快照不写入

- **WHEN** 恢复请求里的快照含重复页 ID、不属于当前 deck 的页，或轨道内容无法通过校验
- **THEN** 系统 MUST 返回 `422 restore_ineligible` / `409 batch_conflict`，
      MUST NOT 改动任何一页

### Requirement: 前端不得把未经核验的批量结果显示为成功

编辑器 SHALL 在页列表提供多选态与「套用当前页动效」入口，并 MUST 在显示成功前
复核响应自洽（`applied` 汇总 = 逐页 `applied` 条数 = 快照条数，且三者非零）。
撤销按钮 MUST 仅在通过快照自检后可用。预检被拒时编辑器 MUST 展示逐页原因汇总，
MUST NOT 只报一句笼统的操作失败。错误兜底文案 MUST 使用已在中英文案表中登记的键。

#### Scenario: 汇总与逐页结果不一致

- **WHEN** 后端返回 `applied=5` 但 `results` 里只有 3 条 `applied`，或只给了 3 份快照
- **THEN** 编辑器 MUST NOT 显示「已套用」，MUST 显示无法核验的提示，且撤销按钮 MUST 保持禁用

#### Scenario: 预检被拒展示可行动原因

- **WHEN** 后端返回 `422 batch_ineligible`，逐页原因为 1 条 `duration_unknown` 与 2 条 `locked`
- **THEN** 编辑器 MUST 明确说明「本次没有改动任何一页」并列出这两类原因及条数

#### Scenario: 多选态不改变单页编辑器行为

- **WHEN** 用户处于多选态并点击左侧页轨的某一页
- **THEN** 该页 MUST 只切换勾选状态，MUST NOT 切换当前编辑页；模板页 MUST 保持为进入多选时的当前页
