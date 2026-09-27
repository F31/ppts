# SLO 与告警 Runbook

> 日期：2026-09-27　|　Phase 2-A5
> 本文所有指标名**逐一取自代码**（`internal/observability/metrics.go`、`prom.go`），
> 不是凭通用模板写的清单。可用 `curl -H "Authorization: Bearer $PPTS_METRICS_TOKEN" <host>/metrics` 核对。

## 0. 先说清楚这些数字的性质

**下列阈值是初始建议值，没有历史数据支撑。** 本系统尚未接入长期指标存储，
没有 p50/p95 基线可用，因此这里的数字只是"先把报警装上"的起点，不等于已达成的承诺。

上线后必须做的第一件事：跑满一个业务周期（至少一周），用真实分布回填下表，
把"初始值"改成"校准值"。在此之前，任何"SLO 达成率"都没有意义。

这也意味着：**当前阶段告警宁可偏松，不要偏紧。** 阈值过紧会训练值班人员忽略告警，
比没有告警更糟（假告警疲劳是真实失效模式）。

---

## 1. 现有指标清单（告警规则的唯一依据）

| 指标 | 含义 | 来源 |
|---|---|---|
| `ppts_worker_jobs_total` | 任务终态计数（按状态分组） | metrics.go |
| `ppts_worker_job_duration_ms_total` | 任务执行耗时累加 | metrics.go |
| `ppts_worker_queue_wait_ms_total` | 排队等待耗时累加 | metrics.go |
| `ppts_worker_queue_oldest_wait_seconds` | **当前队列中最老任务的等待秒数** | metrics.go |
| `ppts_tts_synthesis_total` | TTS 合成调用数 | metrics.go |
| `ppts_tts_throttled_total` | 被供应商限流次数 | metrics.go |
| `ppts_tts_cache_hit_total` | 缓存命中次数 | metrics.go |
| `ppts_tts_fake_provider_active` | **假 TTS provider 是否在用** | metrics.go |
| `ppts_render_pages_disabled` | **渲染是否处于禁用态** | metrics.go |
| `ppts_auth_dev_headers_active` | **开发身份头是否放行** | metrics.go |
| `ppts_auth_events_total` | 认证事件分布 | prom.go |
| `ppts_textnorm_dict_events_total` | 文本规范化词典事件 | prom.go |

另有 `/healthz`（GET，返回 JSON）在 Phase 2-A1 后**真正探测**数据库与对象存，
不可用时返回 503 —— 在此之前它恒返回 200，据此做的可用性 SLO 全是假的。

---

## 2. 硬约束告警（不是 SLO，永远不得触发）

这一节与安全边界同源：**降级状态必须可见**，不能有一部分失效静默运行。
它们没有"容忍度"可言，出现即处理。

| 告警名 | 条件 | 为什么是硬约束 | 处置 Runbook |
|---|---|---|---|
| `FakeTTSProviderActive` | `ppts_tts_fake_provider_active > 0` | 假 provider 意味着**合成出的音频不是真实语音**，用户会拿到看起来正常实则错误的结果 —— 正是"降级态输出与正常态不可区分" | [降级态识别与恢复](#4降级态识别与恢复) |
| `RenderPagesDisabled` | `ppts_render_pages_disabled > 0` | 页面渲染被禁用时可能是"渲染器缺失仍继续解析"的放松行为，产物不可信 | [降级态识别与恢复](#4降级态识别与恢复) |
| `AuthDevHeadersActiveInProd` | `ppts_auth_dev_headers_active > 0`（生产环境） | 任何人可伪造身份头冒充任意租户，属最高危；与 `PPTS_AUTH_DEV_HEADERS` 同源 | [生产上线-认证与TLS](./生产上线-认证与TLS.md) |

> 注：`auth_dev_headers_active` 在本地/容器开发编排里是**预期值**（compose 显式设了
> `PPTS_AUTH_DEV_HEADERS=true`）。这条告警只在生产 profile 上启用，切勿在所有环境一刀切，
> 否则本地开发会被持续告警淹没。

---

## 3. 可用性与容量告警（初始值，待校准）

| 告警名 | 条件 | 说明 | 处置 Runbook |
|---|---|---|---|
| `HealthzFailing` | `/healthz` 连续 3 次非 200 | 依赖不可用。A1 之前这条**不存在**（端点恒 200） | [依赖不可用处置](#5依赖不可用处置) |
| `QueueStalled` | `ppts_worker_queue_oldest_wait_seconds > 3600` | 队列停滞 1 小时：可能是 worker 全挂或任务卡在某个步骤。这个值比"队列长度"更能反映用户感知 | [worker-crash-and-unknown-result](./worker-crash-and-unknown-result.md) |
| `JobFailureRateHigh` | 5 分钟内 `failed` / 总数 > 0.2 | 成功率突降通常意味着外部依赖或数据形态变化；比例而非绝对数，避免低峰误报 | [worker-crash-and-unknown-result](./worker-crash-and-unknown-result.md) |
| `TTSThrottledSustained` | 10 分钟内 `ppts_tts_throttled_total` 增长 > 50 | 供应商持续限流，表现为"任务能跑但很慢"，不告警的话只会被当成系统慢 | [降级态识别与恢复](#4降级态识别与恢复) |
| `MigrationLockContended` | 迁移日志出现 `another instance is migrating` | 多副本同时迁移；一般有副本等一下就过，持续出现说明有实例卡在迁移中 | [多实例与对象存储](./多实例与对象存储.md) |

---

## 4. 降级态识别与恢复

**先确认是哪一种降级，再动手。** 三种降级的信号与后果完全不同：

| 信号 | 含义 | 后果 | 恢复动作 |
|---|---|---|---|
| `ppts_tts_fake_provider_active` | TTS 配成了假 provider | 产物是伪造音频，但流程一切"正常" | 配置真实网关（`internal/gateway` 的模型网关配置），**已生成的音频需重新生成** |
| `ppts_render_pages_disabled` | 渲染不可用 | 缺页面图，导出/预览可能缺内容 | 安装 soffice/pdftoppm（见 `Dockerfile.worker` 的依赖清单），对受影响任务重跑 |
| `ppts_tts_throttled_total` 持续增长 | 供应商限流 | 变慢但不算错 | 降并发或联系供应商提额；**不要**用重试猛打，会加剧限流 |

### 关键判据

恢复降级后，**已经产出的内容不自动变正确**。假 TTS 期间生成的音频不会因为你换回真实
provider 就自己修正 —— 必须显式重跑受影响的任务。这是"降级要有开关、告警、产物留痕三件套"
里"留痕"的实际用途：没有受影响任务清单，就只能全量重跑。

---

## 5. 依赖不可用处置

`/healthz` 返回 503 时，响应体形如：

```json
{"status":"unhealthy","checks":{"database":{"state":"down"},"object":{"state":"up"}}}
```

**响应体只有分项状态，不含错误原因** —— `/healthz` 免鉴权，错误原文只进服务端日志。
这是刻意的：一个对全网可见的端点不该回吐连接串或路径。排障请查服务端日志里的
`healthz: <name> check failed:` 行。

| `checks` 内容 | 含义 | 动作 |
|---|---|---|
| `database: down` | 数据库不可达 | 先查实例与连接数；多副本同时报说明是库侧问题，单副本报说明是网络或凭据 |
| `object: down` | 对象存不可写 | 检查挂载/权限。注意本地适配器探活会**实际写一个临时文件再删除**，可写失败才是 down |
| `object: unsupported` | 该后端无法探活 | **不是故障**，但也**不代表健康**。别把它当 up 用；需要覆盖时应给适配器实现 `Ping` |
| 所有分项缺失 | 本次部署没有这些依赖 | SQLite profile 下无连接池属正常 |

> `unsupported` 与 `up` 的区分是有意的：把"没法检查"并入"健康"，会让端点对一个从未被
> 真正检查过的依赖报平安。

---

## 6. 与既有 Runbook 的对应（双向核对）

| Runbook | 覆盖的告警 |
|---|---|
| [worker-crash-and-unknown-result](./worker-crash-and-unknown-result.md) | `QueueStalled`、`JobFailureRateHigh` |
| [生产上线-认证与TLS](./生产上线-认证与TLS.md) | `AuthDevHeadersActiveInProd` |
| [多实例与对象存储](./多实例与对象存储.md) | `MigrationLockContended`、`HealthzFailing`（对象存分支） |
| [backup-restore-drill](./backup-restore-drill.md) | （数据类，非实时告警，按演练周期触发） |

**维护约定**：新增告警必须在上表登记并指定处置 Runbook；反之亦然。
只登记一侧会导致"告警响了没人知道怎么办"或"写了手册但从没人报警"——
Phase 1.5 的路由清单已经证明手写清单会腐烂，这张表同样会，所以两边同表。
