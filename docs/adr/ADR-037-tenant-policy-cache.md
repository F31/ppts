# ADR-037：租户策略走进程内 TTL 缓存，写路径同进程强失效

- 状态：已实现
- 日期：2026-11-08
- 相关：`docs/优化方案-2026-11-08.md` §P3；`internal/tenant/policycache.go`；ADR-030（配置读取）

## 背景

`tenants.policy` 是一份 jsonb，读它要走 `SELECT policy FROM tenants WHERE id=$1`。
这个读不在管理面上，而在**每一次对象读写**上：

```
objects.Put/Get
  → encryptionStore.enabled()            → ObjectEnvelopeEncryption() → GetPolicy
  → registry.storeFor()                  → ObjectStoreBackend()       → GetPolicy
```

一次对象操作两次策略读，是实测出来的（`WithEnvelopeEncryption(registry)` 链路上
一个 Put / 一个 Get 各触发 2 次 resolver 调用）。一次导出/配音动辄几十到上百个对象
操作，于是同一个租户、同一份不变的政策，在一次请求里被重复查了几十上百次。

基线（本机，2026-11-08，3000×/1000× 各 3 轮取中位）：

| 场景 | 未缓存 | TTL 缓存 | 倍数 |
|---|---|---|---|
| SQLite（本地单租户） | 6.6 µs/op，23 allocs | 28 ns/op，0 allocs | ≈ 235× |
| PostgreSQL（WSL，localhost） | 130 µs/op，10 allocs | 0.6 µs/op，0 allocs | ≈ 200× |

按每次对象操作 2 次策略读折算：一次 100 个对象操作的导出，在 PG 上原本要花
约 26 ms 在「读同一行配置」上，且全部压在连接池上（与业务查询争同一个池）。

## 决策

### 1. 缓存挂在 store 上，不另包一层装饰器

策略读有四个入口：`GetPolicy`、`ObjectStoreBackend`、`StorageRegion`、
`ObjectEnvelopeEncryption`。装饰器方案要么把四个方法全部转发一遍，要么被调用方绕过
（`cmd/ppts` 把原始 store 直接塞进 `objectstore.Registry` 和 `WithEnvelopeEncryption`，
只要有一处漏包装，缓存就形同不存在，而没有任何判据能发现）。

所以缓存放进 `PGStore` / `SQLiteStore` 内部：四个入口都经由 `GetPolicy`，天然共用一份缓存。
两套存储共用一个 `policyCache` 类型，各自只提供「未命中时怎么读一行」的 `fetchPolicy`——
TTL、合流、失效的语义只有一份实现。

### 2. 三条语义约束

- **错误不缓存**。租户不存在、数据库抖动都必须在下次调用重新判定。否则一次瞬时故障
  会被放大成 TTL 长度的持续失败——本来是 1 次报错，变成 30 秒内所有请求都报错。
- **并发未命中合流**。同租户的并发未命中只放一个请求进 `fetch`，其余等结果。冷缓存
  （或刚失效）的瞬间，否则 N 个并发请求会打出 N 次相同查询——缓存反而放大了峰值。
- **跟随者不继承 leader 的失败**。leader 的 ctx 取消（客户端断开）与跟随者的请求无关，
  跟随者看到 leader 出错后自己重试一次，而不是把 `context canceled` 当成结论。

### 3. 失效：同进程强一致，跨进程靠 TTL

`SetPolicy` 成功后立即丢弃该租户的缓存项，所以同进程「改 → 读」是强一致的
（SQLite 单进程部署下即全局强一致）。

多副本 PG 部署无法跨进程失效：A 实例改策略，B 实例最多等一个 TTL 才看到。
这是本决策的**已知代价**，用 TTL 上界兜住，不做 pub/sub 失效（那会把一个读缓存
变成一条新的消息链路，收益不抵复杂度）。

TTL 默认 30s（`tenant.DefaultPolicyCacheTTL`），由 `PPTS_TENANT_POLICY_CACHE_TTL`
配置，`PPTS_TENANT_POLICY_CACHE=false` 回到直读。30s 的依据：策略是管理面配置、
改动由运营发起、容忍秒级延迟；而它含 `envelope_encryption` 这类安全开关，不能给到分钟级。

### 4. 返回值是共享值，只读

缓存命中直接返回同一个 `*Policy`（不拷贝：拷贝 23 个字段换来的隔离，还不如把契约写清楚）。
调用方只能读；改策略必须构造新值走 `SetPolicy`。就地改字段会污染缓存并与其他请求的读并发冲突。
当前全部调用点都是只读（配额、策略展示、动画门禁、加密开关、路由）。

## 被否掉的方案

| 方案 | 否掉的理由 |
|---|---|
| 请求级缓存（把 policy 放进 ctx / 请求对象） | 四个入口只拿得到 `ctx`，要在 `objectstore` 端口上开一个「策略」参数，等于把租户策略泄进对象存储端口的签名；且 worker 侧没有「请求」这个概念 |
| 用 `singleflight` + 第三方 TTL 库 | `golang.org/x/sync` 目前是 indirect 依赖，为一个 map 查找引 direct 依赖不划算；手写 40 行足够，且「跟随者不继承 leader 错误」这条 local 语义 singleflight 不提供 |
| 只在加密层加缓存 | 只省掉两次中的一次，且把缓存语义绑在可选组件上（未配密钥时不构造该层，缓存也随之消失） |
| 把 policy 摊平成独立列 + 每进程定期全量刷新 | 迁移成本高、丢 jsonb 的「新增键无需迁移」优势；收益只是把「每 TTL 一次行读」变成「每 TTL 一次表读」 |

## 判据（可复跑）

```bash
go test ./internal/tenant/ -run 'TestPolicyCache|TestSQLiteSetPolicyInvalidatesCache' -v
go test ./internal/tenant/ -run xxx -bench 'BenchmarkGetPolicy' -benchtime 3000x -count 3
PPTS_TEST_DATABASE=... go test -tags=pg ./internal/tenant/ -run 'TestPGSetPolicy' \
  -bench 'BenchmarkGetPolicyPG' -benchtime 1000x -count 3
```

用例覆盖：TTL 内只查一次、租户隔离、并发合流（64 goroutine → 1 次 fetch）、
到期重读、错误不缓存、跟随者重试、TTL=0 直读、nil 接收者退回直读、
SQLite/PG 两侧 `SetPolicy` 立即失效。

变异验证（每条都必须让用例变红）：删掉 `SetPolicy` 的 `invalidate`、
让 TTL 检查恒真、遮掉合流分支、去掉跟随者重试、无条件写缓存。
唯一存活的是「删掉 `err == nil` 但保留 `p != nil`」——生产 `fetchPolicy` 不会返回
「非 nil 策略 + 非 nil 错误」，该形态不可达，故不为此造用例。

## 重估条件

- 策略读的 QPS 降到可以忽略（例如对象操作全部批量化、每次请求只剩 1–2 次读）→ 缓存收益消失，可删；
- 需要「改策略秒级跨副本生效」→ 换 pub/sub 失效，或把安全相关开关（`envelope_encryption`）单独走不缓存的读；
- 出现「策略里带频繁变化的字段」（如动态配额倒计时）→ 该字段必须拆出，不能共享这份缓存。
