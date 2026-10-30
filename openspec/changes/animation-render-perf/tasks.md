# 任务：渲染性能 M5（片段缓存 + 增量重渲 + 页级并行）

> 判据统一写在每条任务里；没有实测值的勾不算数。

## 1. 缓存键

- [x] 1.1 `internal/render/segkey.go`：`SegmentKeys(plan, SegmentEnv) []string`。
      键 = SHA-256(IR 版本 + 画布/fps/底色 + 字体 + 环境串 + 本页规范化 JSON + 前一页规范化 JSON)；
      前一页只在「本页转场生效」时入键。规范化时清空 `index`/`slideId`/`plateImage`/`layer.image`
- [x] 1.2 用例 9 条：改一页只失效该页与其后继；页序号不参与键；无转场页不吃前一页；
      第 0 页带转场不等于后继页；改前一页使转场页失效；改渲染器版本/CRF/Chrome 使全部失效；
      nil 与 `[]` 层列表同键；0ms 转场等同 none；派生字段与审计字段不进键
- [x] 1.3 用例：键是 64 位小写十六进制、同输入两次相同、不同页不同键
- [x] 1.4 环境串改 JSON 序列化：`k=v;` 拼接会撞键（`Encoding` 自己就是分号分隔的），
      由 `TestSegmentEnvStringIsUnambiguous` 发现并锁住

## 2. 缓存存储

- [x] 2.1 `internal/render/segmentcache.go`：`SegmentCache` 接口（`Get`/`Put`）+ `DirSegmentCache`。
      目录布局 `root/<key[:2]>/<key>.mp4`，写入走「临时文件 + rename」原子落地
- [x] 2.2 容量上限：`MaxBytes` 超限时按 mtime 从旧到新淘汰到 90%，不删本次刚写入的项
- [x] 2.3 用例 6 条：往返字节相同且不改源文件；未命中不留 dst；非法键（`../`、大写、短）拒绝；
      并发写同一键不产生半个文件；超限淘汰到水位且保留最新一条；空文件拒绝入缓存
- [x] 2.4 Windows 的 `rename` 争用（`Access is denied`）：重试 + 「目标已是非空文件即成功」。
      由 `TestDirSegmentCacheConcurrentPutIsAtomic` 抓到（Linux 上跑不出来）

## 3. 服务接线

- [x] 3.1 `ServiceConfig.SegmentCache`（nil = 关闭）。`run` 内先算键、查缓存、得出未命中页集合；
      全命中时跳过 staging 与 backend
- [x] 3.2 `PageRenderer` 可选接口（`RenderPages`）：`RemotionBackend` 与 `StubBackend` 实现；
      不支持该接口的 backend 退回整片 `Render`（此时不查缓存，避免「复制一份又被覆盖」）
- [x] 3.3 staging 限定范围：只复制未命中页 ∪ 转场依赖的前一页的底图；底板/层图同集合
      （`TestStageAssetsOnlyForRenderedPages` 断言集合恰好是 `[p1,p2,p3]`）
- [x] 3.4 回填：只对「实渲且未降级」的页 `Put`；`Get` 命中后做容器校验，失败按未命中
- [x] 3.5 `JobStatus` 增加 `cachedPages` / `renderedPages`
- [x] 3.6 用例 8 条：全命中（backend 零调用）、部分命中（只收到 `[5,6]`）、截断缓存重渲、
      缓存全坏仍成功、降级页不入缓存、关闭缓存渲全部、计数正确、命中页不被新内容串页

## 4. 渲染器（`renderer/render.mjs`）

- [x] 4.1 `--only-pages 0,3,5`：只渲染这些页；未列出的页不产出片段，也不计入 `done`
- [x] 4.2 `--page-concurrency N`（默认 1）：N 个 worker 分摊页；单页降级/重试在页内闭合
- [x] 4.3 并行下事件不串页：进度按「已完成页数」计数（Go 侧同样改为计数而非页序号）
- [x] 4.4 `node --check` 通过、`degrade-check.mjs` 7/7 通过

## 5. 性能门禁

- [x] 5.1 `cmd/render-perf`：自造 10 页 / 30 秒 / 1080p30 夹具（内容寻址页图 + 每页一条元素动画
      + 页间 fade），起真实 `Service`（RemotionBackend + DirSegmentCache），输出 JSON
- [x] 5.2 场景 `second`：冷渲染 → 新 Service（等价进程重启）再渲同一计划。
      **实测 2.4s**（渲染 0.4 + 拼接 2.0），判据 ≤10s PASS
- [x] 5.3 场景 `edit`：改第 5 页后命中 **8/10 = 80%**，判据 ≥70% PASS
- [x] 5.4 场景 `cold` 5 次样本：2 位配置 **P95 133.9s（不达标）**；
      4 位 `2×2` **71.6s**；4 位 `4×1` **49.3s**（推荐配置，PASS）
- [x] 5.5 一致性：冷渲染 vs 关闭缓存 10/10 片段 SHA-256 相同；命中 vs 冷渲染 10/10 相同；
      成片 30.00s / 900 帧一致；增量渲 vs 同一份新计划的冷渲染 10/10 相同
- [x] 5.6 页级并行：`4×1`/`2×2` 与串行的逐页 SHA-256 相同；实测 115.2s → 70.9s（1.62×，页并发 2）
- [x] 5.7 底图级缓存否决测量：`BenchmarkStageSinglePage` = **86.0ms/页**，
      占单页渲染（6.5–9s）约 1% → ADR-4 否决
- [x] 5.8 成片级缓存否决测量：全命中尾部（拼接 + 混音）**2.0s** → ADR-5 否决
- [x] 5.9 结论落部署文档：默认 2 位达不到 120s，推荐 `-concurrency 1 -page-concurrency 4`

## 6. 归档

- [x] 6.1 `design.md` 写 ADR-1…ADR-9，含三组实测与被否决的两个方案（页内拆段、编排层算键）
- [x] 6.2 能力规格吸收进 `openspec/specs/render-plan`；路线图 M5 行改为已归档
- [x] 6.3 `go build ./... && go vet ./... && go test ./internal/... ./cmd/...`（42 包全绿）
- [x] 6.4 `openspec validate --all`
- [x] 6.5 部署文档：compose 的 `-segment-cache` / `-page-concurrency` 说明与达标配置

## 已知未做（登记，不假装完成）

- **默认值仍是页并发 1**：改默认值取决于部署形态（4 vCPU 容器 vs 2 核边端），
  留给 M4 的配额/灰度一起定。现在靠部署文档给达标配置。
- **跨机器共享只验证到「同一目录」**：`SegmentCache` 是按对象存储语义定义的接口，
  S3/MinIO 适配器未实现（当前单机/共享卷够用）。
- **缓存目录无后台清理协程**：容量淘汰只在 `Put` 时触发；长期不写入就不会收缩。
- **出场动画在夹具里没有**：10 页夹具只有入场动画，所以「少 staging 前一页」的像素后果
  在这份夹具上不可见；用 `TestStagePagesIncludesTransitionPredecessor`（结构）+
  `edit-parity`（端到端）守住，而不是靠夹具覆盖。
