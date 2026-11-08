# Design: 作者侧成品分享控制台与观看统计

## 0. 排序依据（路线图剩余缺口复核）

| 候选 | 依赖是否就位 | 能否纯代码闭环 | 结论 |
|---|---|---|---|
| **作者侧成品分享控制台 + 观看统计** | 是：M6-3 后端、V20 事件、Library 页都已归档 | 是 | **本变更**。V20 的 Out of Scope 直接点名；且它是唯一能把已落地的两条链接成用户可见成果的缺口 |
| V15 字体子集 / WebP / AAC | 需资产与产品决策（字体许可、编码可用性） | 否 | 后移。当前 2.05 MiB / 预算 3.28 MiB 仍有余量，不是瓶颈 |
| 移动端真机 V16 | 需真机设备 | 否 | 后移，属验收条件而非代码缺口 |
| 额度剩余量 UI / 自助提额 | M4 后端已就位 | 是 | 后移，运营面价值低于「作者能不能把成品分享出去」 |
| Q7 真人讲稿重测 | 需外部素材 | 否 | 长期挂起，与代码无关 |

**不新增门禁编号**：V21 空闲，但本变更是功能接线与查询能力，不属于 M6-4 的判据序列；
硬编一个 V21 会与保真链已有的两套 V20 编号体系继续叠加。引用一律用变更名。

## 1. 为什么在 SQL 里聚合，而不是 `List` 之后在 Go 里数

`audit.Store.List` 的 `Limit` 被夹在 `[1,500]`，按 `created_at DESC` 只回最近 500 条；
`Filter` 也没有 `ResourceID`。一个热门链接几天的事件就能超过 500 条，用它做统计会
**静默低报**——数字看起来完全正常，只是少了后面一大截。这比报错更坏。

因此 `audit.Store` 增加 `ViewStats(ctx, tenantID, resourceID, since)`，两边各写一条
聚合 SQL，只回 8 个标量，不搬事件列表：

- SQLite：`json_extract(metadata,'$.event')` / `json_extract(metadata,'$.session')`
  （modernc.org/sqlite 1.59 自带 JSON1，已实测 `json_extract` 可用）。
- PG：`metadata->>'event'` / `metadata->>'session'`，查询仍包在 `tenant.Run` 里，
  RLS 与 `List` 同一条纪律。
- 计数一律 `COUNT(DISTINCT session)`，不用事件条数：播放器重试、`pagehide` 冲刷
  与 400ms 微批都可能让同一会话重复落同名事件，按条数会把刷新算成新观看。
  `view` 另给一个**按会话去重**的曝光数，与「独立会话总数」分开。
- 过滤条件写死 `action = artifact_share.view_event` 与
  `resource_type = artifact_share_link`，避免把 `anonymous_access`、撤销审计等
  别的操作数进漏斗。
- 事件名白名单在 SQL 里用 `CASE WHEN`，未知 `event` 一律不计入任何桶——将来
  加新事件时看板不会凭空多出观众。

`Store` 接口加方法会动到两个测试 fake（`fakeAuditStore`、`fakeArchiveStore`），
与 `shareAuditStub`；后者改为内嵌真实 `*audit.SQLiteStore`，这样统计端点测试读到的是
**真库真 SQL**，而不是再写一份「按我理解」的假聚合。

## 2. 统计口径：四件事必须在 UI 上说清

1. **范围**：只统计在线 `/s/<token>/` 观看。离线包、`file://`、隐私模式、DNT/GPC、
   `?privacy=1` 本来就零请求，这不是「没人看」。
2. **留存窗口**：`audit_events` 会被 `Archiver.ArchiveBefore` / `DeleteBefore` 清理；
   端点按 `days`（默认 30、上限 90）截窗，返回 `window.since/until` 与
   `retentionNote`。面板说「最近 N 天」，不说「累计」。
3. **采集开关**：`PPTS_SHARE_TELEMETRY=0` 时端点仍返回 200，但带
   `telemetryEnabled:false`；面板显示「服务端采集已关闭」，**不把 0 显示成 0 次观看**。
4. **完播率分母**：`progress_100` 的独立会话数 / 本窗口独立会话数，并在分母旁标注。
   100% 桶会因拖动跳过而不补报（V20 的诚实判据），所以分母不是「打开过的人数」
   的完美代理，面板不把它写成「完播率（精确）」。

响应不返回 `session`、token、IP、UA 或任何单条事件；聚合结果本身不比 V20 的
匿名承诺多泄露任何东西。

## 3. 鉴权与归属

`GET /projects/{pid}/artifacts/{aid}/shares/{linkId}/stats` 与既有三条成品分享端点
同组挂载，复用 `loadShareableArtifact`：

- `requirePrincipal` + `requireRole(editor)` + `requireProjectAccess`；
- 成品必须属于路径里的项目，不匹配 404（不泄露「这个 ID 存在但不在你项目」）；
- 链接通过 `ListShareLinks(tenant, artifactID)` 取回后按 `linkId` 匹配，找不到 404；
  不新增「按 ID 直取」的读口，避免绕过成品归属校验。

匿名上报端点保持原样，本变更不碰 `/s/`。

## 4. 前端接线形态

`Library.tsx` 的成品卡片对 `html_bundle` 显示「分享」图标按钮（`loadShareableArtifact`
明确只允许 `html_bundle` 在线分享，`html_single` 虽与 bundle 共用组装但不能建 `/s/` 链接，
因此不给它按钮——不留「点了才报错」的假入口）；点击打开 `ArtifactSharePanel` 模态框：

- 顶部固定一句范围说明（在线分享 / 离线与隐私不上报 / 留存窗口）；
- 链接列表按创建时间倒序，每条给复制链接、撤销、展开数据；
- 创建表单：访问模式、有效期（天）、可选口令，走既有 POST 端点；
- 观看数据只在展开时请求，四个进度桶画成横向条，宽度以**独立会话数**为基准；
- `telemetryEnabled:false` 时用说明条替换整个数据区，不渲染一排 0；
- 请求失败给出原因，不把「加载失败」渲染成「0 次观看」。

网络请求全部收在 `web/src/api/artifactShare.ts`（`api-contract-check` 只放行
`src/api/*.ts`），组件不出现 `fetch`。所有新路由登记进 `route-inventory.json`。

## 5. 门禁不空过

- Go：聚合 SQL 对多会话 / 重复事件 / 跨链接 / 跨租户 / 窗口外 / 未知事件名 / 别种
  `action` 各自可区分；变异「去掉 action 过滤」「按条数不按会话去重」必须红。
- Handler：非 editor、成品越项目、链接不属于成品、无审计能力、days 越界各自可区分。
- 前端静态门禁：`artifact-share-console-check.mjs` 断言组件里没有 `fetch`、
  四个诚实口径文案确实被渲染、采集关闭分支不渲染数字区、API 路径与 Go 路由表一致；
  用例为 0 时 exit 2（与 V18/V19/V20 同一纪律）。
- 真机：8077 上「建链 → 匿名上报 → 统计端点」数字必须对得上，且撤销后旧链接不产生新数据。

## 6. 边界

- 不改 `audit_events` 表结构、不加索引以外的迁移（既有 `idx_audit_events_tenant_action`
  对 `(tenant_id, action, created_at)` 已可服务本查询；本次不新增迁移文件）。
- 不做跨成品汇总、时间趋势、CSV 导出。
- 不因为看板放宽 V14 零外链或 V20 的 CSP 交集。
