# Design: 在线分享观看埋点（V20）

## 1. 编号为什么是 V20 而不是 V18

设计初稿的 §20.8 表格止于 V18「埋点完整性」。实现期先落地的却是另两条门禁：
`scripts/review/shot-gate.py`（V18 切图，三个取样点）与 `scripts/review/seam-gate.mjs`
（V19 页缝延续）。两条都在 `.github/workflows/ci.yml` 里以编号命名，脚本、注释、
夹具、归档文档都按编号互相引用。

改它们的编号要同时改 CI 步骤名、脚本注释、6 份归档 spec、历史工作日志，且没有任何
功能收益；改「还没实现的埋点」只需改设计文档的三处引用。因此埋点顺延为 **V20**，
并把 §20.8 表格补成与实际 CI 一致的 V13–V20（含 V13b）。**门禁函数名与档案例外地
保留 V18/V19 原样**，这不是遗漏，是刻意的低成本收敛。

> **命名空间边界**：V20 只在 M6-4 分享包门禁体系（§20.8 的 V13–V20）内唯一。
> 保真链 `scripts/fidelity/verify.py` 另有一组 V20（矩形焦点遮罩，判据码同样形如
> `V20.x`，`check_speech_gate.py` 还有一组同号判据），两套编号不共用命名空间。
> CI 步骤名、门禁报告与文档引用都须带体系名，避免把「V20.1 埋点符号」和
> 「V20.1 白纱强度」混成一条判据。

## 2. 边界：什么算「在线分享」

播放器判据在 `web/src/sharePlayer/telemetry.ts`，四个条件全部满足才启用：

```
protocol ∈ {http:, https:}
pathname 严格匹配 /s/[0-9a-f]{32}/        ← 成品级匿名分享的唯一形态
DNT != '1' 且 GPC != true
search 里 privacy 不是 1/true
```

为什么用**路径形态**而不是「服务端注入一个开关」：
在线 `/s/` 与离线 `file://` 读的是**同一份字节**。注入会改 `index.html`，破坏
`ShareLink.ContentHash` 快照语义（旧链接必须钉住字节，见 M6-3），也会让打包时
的 V14 自检与运行时内容不一致。路径是浏览器自己就知道的事实，不需要注入。

这条判据同时保证 V16「无 404 / 无 console error」：`bundle-gate.py` 用 `file://`
打开包（或 `serve.py` 的 `/` 路径），两种都不匹配，因此**一个请求都不会发**。
判据放在 `startShareTelemetry()` 的最前面，不启用时直接 `return null`，连 `fetch`
都不构造。

## 3. CSP 交集：真正的冲突点

| 来源 | connect-src |
|---|---|
| `internal/share/html.go` 的 meta CSP | 原来没有 → `default-src 'none'` |
| `artifact_share_serve.go` 的响应头 CSP | `'self'` |

浏览器对同一文档的多份 CSP 取**交集**，所以只给响应头加 `'self'` 不够：meta 那份
仍会把 fetch 拦下，而且表现为「什么都没发生」（埋点失败本来就是静默的，最难查）。

修改只是给 `indexCSP` 补 `connect-src 'self'`：

- 不含任何远程来源，`connect-src` 里没有 `*`、没有 `https:`；
- V14 判据扫的是 `src=` / `href=` / `url()` 的**引用目标**，CSP 指令不是引用，
  `share-verify` 的外部引用数仍是 0；
- 离线 `file://` 下这条指令不会产生请求（播放器判据已关），`file:` 页面本来也
  发不出同源 fetch。

`form-action` 仍是 `'none'`（meta）与 `'self'`（响应头）的交集，不受影响。

## 4. 事件模型：为什么复用 `audit_events`

不新建 `artifact_share_events` 表，理由三条：

1. 匿名端每多一张可写表就多一个写放大面，PG 还要为它设计 `anon_insert` RLS 策略；
   而 `audit.Record` 走 `tenant.Run` 设上下文，已有 `artifact_share.anonymous_access`
   这条匿名写入先例。
2. 观看事件是「有人看了这一版成片」这条运营事实的一个维度，不是新业务实体。
3. 脱敏纪律现成：ResourceID 用 `link.ID` 而非 token，Metadata 只有
   `surface / session / source / event / project_id / artifact_id`。`WithClientIP`
   只在认证路径注入，`/s/` 不经过它，所以不会意外带上 `client_ip`。

`sessionId` 是播放器为这一次观看生成的 16 字节随机值（32 位小写 hex），
不与任何账号关联。`source` 只取显式 `utm_source` / `from` 或 **referrer 的 host**，
绝不写完整 URL（可能带查询串、口令、内部路径），限长 64 且字符白名单。

服务端总闸 `PPTS_SHARE_TELEMETRY` 与 `PPTS_AUDIT_ANONYMOUS_ACCESS` 刻意分开：
前者管「要不要采集观看进度」（产品口径，默认开启），后者管「匿名访问要不要进
审计留存」（合规口径，默认关闭）。隐私承诺的第一道在浏览器，总闸是运营侧的第二道。

## 5. 事件集合与「不伪造观看」

封闭集合：`view`、`progress_25`、`progress_50`、`progress_75`、`progress_100`。
后端 `normalizeShareViewReport` 只认这些值，单请求内按 `(kind, bucket)` 去重，
一次最多 8 条、请求体 4 KiB；前端用 400ms 微批 + `keepalive` + `pagehide` 冲刷。

**拖动进度条不算看过中间内容**。`seek()` 调 `markSkippedBuckets` 把跳过的桶标成
「不再上报」，于是「拖到 80% 再播放」不会凭空生成 25/50/75 三条。首帧曝光在
`render(0)` 之后才发（清单损坏提前 return 时不算曝光）；`finish()` 补 100% 并立即
冲刷，避免依赖 rAF 恰好落在终点那一帧。

这是故意不把集合「凑齐」：埋点要说实际发生的事。V20 的「齐全」指的是**该发生的
事件都能被采集到**，不是每次观看都必须凑满五条。

## 6. 匿名写放大的三层防护

| 层 | 措施 |
|---|---|
| 请求 | Content-Type 必须 `application/json`、Origin 同源校验、body ≤ 4 KiB、事件 ≤ 8 且白名单 |
| 会话 | 单请求 `(kind,bucket)` 去重；按 token 进程内固定窗口 40 次/分钟，超限 429 |
| 存储 | 每条事件一条 audit 行；不新增可写表；忽略写入错误，不影响播放 |

限流用进程内 `shareEventThrottle` 而非 PGBackend：匿名端没有租户上下文，而这里防的
只是「一个 token 被脚本无限刷」。多副本下上限按副本数放宽，但每个副本仍有上限，
且 map 有 4096 容量帽与过期淘汰，不会被造 token 撑爆。

所有失败都返回 2xx/4xx 且前端静默：`fetch(...).catch(() => undefined)` +
`try/catch`。埋点是旁路观测，网络抖动绝不能变成播放故障（与 V16 判据直接相关）。

## 7. 门禁分层

| 层 | 位置 | 验什么 |
|---|---|---|
| 行为判据 | `web/share-telemetry-check.mjs` 9 项 | 启用/隐私判据、来源脱敏、跨桶、拖动跳过、事件集合前后端一致、源码接线、CSP、V14 未放宽、编号订正 |
| 服务端契约 | `internal/api/artifact_share_events_test.go` | 五类事件齐全、去重、非法事件零写入、总闸/口令门/限流/Origin/脱敏 |
| 成品包 | `scripts/review/telemetry-gate.mjs` 8 条 → CI | 埋点真的编译进包、CSP 交集、零外链、只有一处 fetch；旧版 app.js 负例 6 红 |

实现期还抓到一个「本地绿、CI 红」的门禁缺陷：编号订正用例最初直接 read 仓库外的
`docs/动画视频扩展-设计方案-V1.0.md`，而 tasks 6.4 已把 `/docs/*` 移出仓库
（`.gitignore` 只放行 `adr/`）。干净检出里没有这个文件，整组 `telemetry:test` 会因
ENOENT 判红。判据改为只锚定随代码分发、且不受 `openspec archive` 移动的三个文件
（`ci.yml` / `telemetry-gate.mjs` / `route-inventory.json`）；设计文档只在**存在时**
做本地补核。已用「临时移走该文档」模拟干净检出实测 9/9 通过。

门禁没有空过路径：V20 脚本 `checks` 为空时 exit 2；Go 与前端用例都断言了「不该写时
一条都不写」。

## 8. 不在本变更内

作者侧统计看板与聚合 API（事件可按 `action` + `metadata.session/event` 直接查）、
MP4/离线包/站内预览埋点、跨站分析与身份关联。这些都不改变本变更的事件契约，
后续加看板时不需要改播放器或端点。
