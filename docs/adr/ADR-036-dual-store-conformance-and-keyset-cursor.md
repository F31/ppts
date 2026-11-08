# ADR-036：双存储契约测试与 (时间, id) 两段式游标

- 状态：已实现
- 日期：2026-11-08
- 相关：`docs/优化方案-2026-11-08.md` §3.1 / §P1

## 背景

SQLite（本地单租户）与 PostgreSQL（生产）是两套独立实现：`ProjectStore` 34 个方法、
`Store`（任务）外加积压指标等一共四十余个，各自一份 SQL。接口只约束签名，
**不约束「查不到时返回哪个错误」「游标到底用没用到」**。

在此之前，两边的测试也是各写各的：SQLite 侧有关系断言，PG 侧只有 handful 个用例，
`var _ ProjectStore = (*PGProjectStore)(nil)` 这种编译断言在 PG 侧根本没有。
结果是语义漂移只能靠人肉 review 发现。

本轮把「差异」当清单逐条验证，发现的都不只是「风格不同」：

| # | 差异 | 后果 |
|---|---|---|
| 1 | `run_at` 零值：PG 写 `0001-01-01`，SQLite 写 NULL | PG 的「最老待领取任务」恒为公元 1 年（P0 已修） |
| 2 | SQLite 缺 `job_events` / `jobs(phase)` 索引 | 读侧全表扫（P0 已修） |
| 3 | 唯一性检查：PG 先 SELECT 再 INSERT、SQLite 靠约束 | 并发下 PG 抛未映射的 23505 → 500 |
| 4 | 错误码：`CreateShareLink` 非法模式，PG 裸 `errors.New`、SQLite 返回 `ErrShareLinkNotFound` | 一个 500、一个「链接不存在」，都不是参数错误 |
| 5 | 写校验：`AttachTag`/`MoveProject`/`InviteCollaborator`，PG 查归属、SQLite 没有 | SQLite 能把别的租户的标签贴上来 |
| 6 | 归档列表游标：两侧形参都没用 | 超过一页后「加载更多」重复同一批 |

真写用例时又抓出四条**只有跑起来才会暴露**的：

| # | 问题 | 症状 |
|---|---|---|
| 7 | PG `ListTags` / `ListFolders` 直接扫可空列（`color`、`created_by`） | 只要有一个标签没配色，整个列表 500 |
| 8 | PG `QueueDepthByKind` 的 `notReady` 写成 `run_at IS NULL OR run_at > now()` | 即时任务同时计入 waiting 与 notReady，积压看板自相矛盾 |
| 9 | PG `DeleteFolder` 删 0 行仍返回成功 | 重复删分组永远 200，SQLite 是 404 |
| 10 | SQLite 建重名分组不报错 | `folders` 表两侧都没有唯一约束，SQLite 侧连预检查也没有 |

## 决策

### 1. 契约测试是这套双存储的**唯一**一致性保证

新增 `internal/project/conformance_test.go` 与 `internal/pipeline/conformance_test.go`：
同一组**纯行为断言**（不出现 SQL、不假设方言、不关心表名），两套环境各自准备。

- SQLite 版直接挂在普通 `go test ./...`（`TestSQLiteConformance` / `TestSQLitePipelineConformance`）。
- PG 版挂在现有 `-tags=pg` 作业（`TestPGConformance` / `TestPGPipelineConformance`），
  每个子用例自建租户，跑完清理，不与同包其它 pg 用例共享状态。

判据不是「跑通了」，而是**变异验证**：把任意一条已修的差异改回去，用例必须红。
本轮逐条验证了 6 条（归档游标恒空、PG notReady 条件、PG run_at 零值、SQLite 跨租户贴标签、
PG tags/folders 可空列），全部被杀掉；其中「ListTags 不兜 NULL」第一次跑时**存活**——
说明用例没覆盖列表读取，补了断言之后才杀掉。这一步是这套测试唯一的可信度来源。

不追求覆盖全部方法（那会变成给每个方法写两遍单测）。当前覆盖：项目可见性/归档/分页/
版本生命周期/标签/分组/协作者/分享链接/组织视图，任务幂等/run_at/领取租约/fencing/
终态/取消/重试/分页/事件流——高风险路径先齐。

### 2. 分页游标是 `(时间, id)`，不是只有时间

排序键只有时间时，同一毫秒内的多行会一起越过边界：上一页把它们截断在中间，
下一页用 `时间 < 游标` 把剩下的全部跳过。SQLite 的时间列是毫秒精度，
批量导入 5 个项目、一键导出 40 页任务都必然撞同一毫秒——丢的正是用户刚做的那批。

- 编码：`<RFC3339Nano>|<id>`（`internal/project/cursor.go`），任务侧沿用已有的
  `encodeJobCursor`（base64，`(sortValue, id)`），不再新造第三种格式。
- SQL：`(t < $t) OR (t = $t AND id < $id)`，`ORDER BY t DESC, id DESC`。
  PG 用 `id::uuid` 比较，文本序与字节序在 UUID 上一致，因此两侧顺序也一致。
- **兼容旧游标**：没有分隔符的旧格式按「只比时间」处理。已经发出去的游标不能因为
  升级一次就变 500。
- 坏游标统一成 `ErrInvalidCursor` / `ErrBadJobCursor`（400）。此前 PG 直接把
  `time.Parse` 的错误往上抛（500），SQLite 把坏游标当零值时间（静默从头翻页）。

### 3. 错误码：查不到 = typed sentinel，参数非法 = 另一个 sentinel

新增 `ErrTagNameRequired`、`ErrFolderNameRequired`、`ErrInvalidShareAccessMode`、
`ErrInvalidCursor`，并在 API 层映射为 `InvalidArgument`。空标签名此前在 PG 上是 500、
在 SQLite 上是「真的建出一个空名标签」（第二次才报已存在）。

PG 的唯一约束冲突补 `isPGUniqueViolation`（23505）映射：先 SELECT 后 INSERT 只是
为了让常见路径给出干净的错误，它挡不住并发；不映射的话并发冲突会变成 500，
而 SQLite 侧一直是 `ErrTagNameExists`。

## 后果

- 两侧行为一致的部分被钉死；不一致的部分必须显式二选一，不能各写各的。
- 变更新实现（换掉 SQLite、加第三个后端）时，先让契约用例通过再谈别的。
- 成本：PG 侧多 20 个用例的建租户/清理开销（本机约 2 秒），CI 增加可忽略。

## 未覆盖 / 已知缺口

- `MarkStep` / `CompleteWithStep` 的步骤覆盖规则（既有 `steps_read_test.go` 两侧各有一份），
  尚未纳入契约集。
- `folders` 表两侧都没有 `(tenant_id, name)` 唯一约束，靠应用层预检查；
  并发建同名分组在两种实现上都会产生重复。要根治得先清存量再加索引，
  留待单独一次数据迁移。
- 租约过期后的重领取（`lease_until < now`）只验证了「未到期领不到」，
  未覆盖时钟推进后的竞态；需要可控时钟，成本高于收益。
