# Proposal: 作者侧成品分享控制台与观看统计

## Why

V20（`share-view-telemetry`）把在线分享的观看事件落进了 `audit_events`，但那份变更的
Out of Scope 明确写着「作者侧观看统计看板与聚合查询 API」。因此到本变更之前，事件
**只进不出**：没有任何端点把 `artifact_share.view_event` 聚合成作者能读的指标，
§20.7 点名的「分享链接的观看完成率」仍然量不出来。

开工前核实到一个更靠前的缺口：`html-bundle-sharing`（M6-3）的成品分享链接
**后端完整、前端零接线**。`GET/POST /projects/{pid}/artifacts/{aid}/shares` 与
`.../revoke` 都在，`web/src` 里却没有任何文件出现这条路径；`Library.tsx` 的成品卡片
只有预览 / 下载 / 删除，作者既建不了链接、也撤不了、更看不到数据。V20 埋点因此挂在
一条作者侧完全不可见的链上——只补统计 API 会得到一个没有入口的看板。

所以本变更把两件事合成一个闭环：**把成品分享接到作者侧 + 把已经采到的事件读出来**。
这不是扩大范围，而是让已归档的两个里程碑（M6-3、V20）真正可用。

## What Changes

- **审计聚合能力**：`audit` 层新增 `ViewStats`，按 `(tenant, resource_id, action,
  created_at >= since)` 在 SQL 里聚合出曝光数、独立会话数与四个进度桶的独立会话数。
  SQLite 与 PG 各写一份方言（metadata 分别是 TEXT/jsonb），PG 仍走 `tenant.Run`。
  **不用 `List` 再在 Go 里数**：`List` 的 500 条上限会让长尾静默截断，看板不能低报。
- **作者侧统计端点**：`GET /projects/{pid}/artifacts/{aid}/shares/{linkId}/stats?days=N`，
  复用 `loadShareableArtifact` 的 editor 鉴权；链接必须属于该成品与租户，否则 404；
  `days` 默认 30、上限 90。响应带 `window` / `telemetryEnabled` / `retentionNote`，
  明确「只统计最近 N 天内仍留存的匿名审计事件」。
- **前端控制台**：`Library.tsx` 的 `html_bundle` 成品卡片加「分享」入口，打开
  `ArtifactSharePanel`：创建（访问模式 / 有效期 / 口令）、列出、复制、撤销链接，
  每条链接可展开观看数据（曝光、独立会话、25/50/75/100 漏斗、完播率）。
- **诚实口径**：采集总闸关闭时显示「采集已关闭」而不是 0；离线包 / 隐私模式 /
  DNT / GPC 不上报这件事写在面板上；完播率分母写明是「本窗口内独立会话」；
  审计归档清理后不可回溯，面板不说「全量」。
- **门禁**：`web/artifact-share-console-check.mjs` 静态钉住诚实反馈与只读边界；
  `route-inventory.json` 登记新路由；Go 侧聚合与 handler 单测；CI 增一步。

## Capabilities

- `html-share-distribution`（新增 3 条 Requirement）：作者侧可管理成品分享链接、
  观看数据可聚合、统计口径不得谎报。

## Impact

- 后端：`internal/audit/view_stats.go`（新）、`audit.go`、`sqlite.go`、`postgres.go`、
  两个 fake；`internal/api/artifact_share_stats.go`（新）、`artifact_share.go`、
  `server.go`、对应测试。
- 前端：`web/src/api/artifactShare.ts`（新）、`api/index.ts`、`types.ts`、
  `components/ArtifactSharePanel.tsx`（新）、`pages/Library.tsx`、`i18n/*.json`、
  `styles.css`、`route-inventory.json`、`package.json`。
- 门禁/CI：`web/artifact-share-console-check.mjs`（新）、`.github/workflows/ci.yml`、
  `openspec/roadmap/动画视频扩展-V1.0-路线图.md`。

## Out of Scope

- 观看者画像、跨站分析、第三方统计 SDK、身份关联（V20 已明确排除，本变更不翻案）。
- 按时间趋势的折线图、跨成品汇总排行、导出 CSV。
- MP4 / 离线 HTML 包 / 站内预览页的观看统计（都不属于在线分享形态）。
- 改 V20 的播放器与事件端点；不改 `audit_events` 表结构，不新增迁移。
- `/p/{publicId}` 公开广场接入（仍需产品先定「成品算不算作品」）。
