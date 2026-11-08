# ADR-038：请求级只读缓存（document.json 与配音时间轴）

- 状态：已实现
- 日期：2026-11-08
- 相关：`docs/优化方案-2026-11-08.md` §P3-3；`internal/api/requestcache.go`；ADR-036（双存储契约）

## 背景

批量动画端点是按页组织的，而每页的上下文解析都要读整份解析产物：

```
apply-all / apply-batch / restore
  → 逐页 AnimationPageResolver.Resolve
      → loadProjectDocuments     读并解码整份 document.json（整份，不是单页）
      → pageDurationMS
          → LatestSucceededJob   SQL
          → StepResultRef        SQL
          → loadBundle           读并解码整份 timeline.json（stat + 读 = 2 次对象读）
```

实测（`countingObjects` 包住真实对象存储，12 页 deck）：一次 apply-all 触发
**48 次对象读**——`12 × (1 文档 + 1 时间轴 stat + 1 时间轴读 + 1 冗余)`。
23 页的成片 deck 就是 92 次，外加 46 条 SQL。这些数据在一次 HTTP 请求里不会变，
全部是重复读；在 S3 类后端上每一次都是一个 RTT。

另有一处独立的浪费：`loadProjectDocuments(revA, revB)` 按 `[revA, revB]` 逐个读，
而除版本 diff 之外所有调用方都传 `revA == revB`，于是**同一份文档被读两遍**——
这条在没有缓存时就已经存在。

## 决策

### 1. 作用域＝一次请求，挂在 context 上

缓存对象由 `withRequestCache(ctx)` 创建、`requestCacheFrom(ctx)` 取出；没有挂载时
所有读路径保持原样直读（缓存是加速手段，不是正确性的前提）。

为什么不用别的三种做法：

- **给 `Resolve` 加参数**：`AnimationPageResolver` 是接口，改签名要动全部测试替身与
  两处装配，而收益与「把 ctx 传下去」完全一样；
- **进程级缓存**：document.json 会随导入变化，进程级缓存必须自己解决失效，而失效
  信号在 worker 里、跨进程——这正是 ADR-037 里靠 TTL 兜住的那种复杂度。请求级没有
  这个问题：请求结束即失效，不可能读到过期数据。
- **装饰器包一层 ObjectStore**：与 ADR-037 同一条理由——被绕过的装饰器静默失效且
  没有判据；而且这里要缓存的不是「某个键的对象」，是「解码后的文档」，装饰器拿不到。

### 2. 三个缓存槽，nil 是结论

- `documents`：`(tenant, project, revision) → *project.Document`
- `timelines`：`(tenant, project) → *app.TimelineAsset`
- 读不到（文档缺失、没有时间轴、解析失败）**也写入缓存**，值为 nil。

负缓存不是顺手加的优化：一次存储抖动若不被记住，同一请求的后续 20 页会各自再超时
一遍，把「一次 3 秒的抖动」放大成「20 次串行超时」。这与
`AnimationPageAssembler.elements` 对几何失败的既有取舍一致。

### 3. 只用于只读路径

缓存里的 `*project.Document` 会在同一请求内被多个 handler 共享，谁改它别人就看到
改后的值，而没有任何编译期约束能拦住。因此：

- 只有批处理入口（apply-all / apply-batch / restore）挂载缓存；
- 单页端点不挂（读一次，没有收益，也就不必承担共享风险）；
- 生产链路上 `PageBindElements` / `PageAnchors` / `DefaultSpecReport` 都只读页面，
  已逐个确认（见代码注释）。

### 4. 页序读取也要在缓存之后

`SlideIDs` 自己也会读一遍文档。apply-all 最初把缓存挂在循环前、页序读取之后，
结果那一次读落在缓存外——实测 12 页仍读 3 次文档。所以缓存必须在**第一次**触碰
文档之前挂好，这点写在调用点的注释里。

## 结果（本机实测，夹具 12 页）

| 场景 | 改前 | 改后 |
|---|---|---|
| apply-all，3 页 | 15 次对象读 | 3 次 |
| apply-all，12 页 | 48 次对象读 | 3 次 |
| apply-batch，12 页 | 48 次对象读 | 3 次 |
| 每条 `LatestSucceededJob` / `StepResultRef` | 每页各 1 次 | 每请求各 1 次 |

「3 次」= 文档 1 次 + 时间轴 2 次（stat + 读）。判据不看延迟看**次数**：
`TestApplyAllObjectReadsDoNotGrowWithSlides` 要求 3 页与 12 页次数相等且 ≤3，
`TestApplyBatchObjectReadsDoNotGrowWithSlides` 守批量路径，
`TestApplyAllReadsDeckOncePerRequest` 守作用域（连发两次请求各自都要读一次文档）。

延迟上的收益取决于后端：本地盘是几毫秒；S3 类后端 12 页省下约 45 个 RTT。

## 已知代价

- 缓存让「同一请求内文档一致性」从「每页各读一次」变成「一次读、后续共享」。若某次
  导入恰好在请求中间完成，旧实现可能读到新版本、新实现一定读旧版本。两者都不是
  「保证一致」，但**语义变了**：现在是快照语义。这与端点本身「按 sourceRevisionNo
  取某一版」的契约一致，故接受。
- 时间轴缓存以项目为键，不含 job id。若同一请求里「最近成功配音任务」变了（只可能
  因为 worker 刚提交），该请求会继续用旧时间轴——同样是快照语义。

## 重估条件

- 出现需要在同一请求内写文档的路径 → 必须改成写后失效，或干脆不开缓存；
- 单页端点也开始需要多次文档读（例如预览要整份 deck）→ 应把缓存挂到 middleware
  统一开启，而不是逐个 handler 记得挂；
- 时间轴已能一次读拿到多版本 → `timelines` 的键要带上版本，否则会串版本。
