# Tasks: 作者侧成品分享控制台与观看统计

## 1. 审计聚合能力

- [x] 1.1 `internal/audit/view_stats.go`：`ViewStats` 结构与事件名常量；`Store` 接口加
      `ViewStats(ctx, tenantID, resourceID, since)`。
- [x] 1.2 SQLite 侧聚合（JSON1 + `COUNT(DISTINCT ...)`），PG 侧聚合（jsonb + `tenant.Run`）。
- [x] 1.3 两个 fake 补齐接口；`shareAuditStub` 内嵌真实 `*audit.SQLiteStore`。
- [x] 1.4 `internal/audit/view_stats_test.go`：多会话、重复事件、跨链接、跨租户、窗口外、
      未知事件名、别种 action、空结果；变异「去掉 action 过滤」「按条数计数」各自红。

## 2. 作者侧统计端点

- [x] 2.1 `internal/api/artifact_share_stats.go`：`stats` handler、`days` 参数（默认 30 / 上限 90）、
      `window` / `telemetryEnabled` / `retentionNote` 响应。
- [x] 2.2 在 `registerArtifactShareRoutes` 注册，复用 `loadShareableArtifact` 与
      `ListShareLinks` 做归属校验；`server.go` 注入不变（Audit 已是 Store）。
- [x] 2.3 `internal/api/artifact_share_stats_test.go`：正常聚合、days 越界、非 editor、
      成品越项目、链接不属于成品、采集关闭、无统计能力、空数据不谎报。
- [x] 2.4 `web/route-inventory.json` 登记新路由并写明归属校验与只读语义。

## 3. 前端控制台

- [x] 3.1 `web/src/api/artifactShare.ts`：list / create / revoke / stats 四个封装；
      `api/index.ts` 转出；`types.ts` 加 `ArtifactShareLink` / `ArtifactShareStats`。
- [x] 3.2 `web/src/components/ArtifactSharePanel.tsx`：范围说明、链接列表、创建表单、
      复制、撤销、按需加载统计、漏斗条、采集关闭分支、错误态。
- [x] 3.3 `Library.tsx` 卡片加分享入口（仅 html 成品）、挂载面板；`styles.css` 补样式。
- [x] 3.4 `i18n/zh.json` / `en.json` 补键，`i18n:check` 双语文案数量一致。

## 4. 门禁与 CI

- [x] 4.1 `web/artifact-share-console-check.mjs`：组件零 fetch、诚实口径四条、
      路径与 Go 路由表一致、空过 exit 2；挂进 `npm run build`。
- [x] 4.2 `.github/workflows/ci.yml` 增加一步，`api-contract-check` 对新增路径放行并校验。
- [x] 4.3 Go / 前端 / 分享包全量门禁：`go build/vet/test ./...`、`npm run build`、
      `build:share`、`i18n:check`、`route:check`、`api:check`、`openspec validate --all`、
      `git diff --check`。

## 5. 真机验证与交付

- [x] 5.1 交叉编译部署 8077 / 8080，health 200；备份旧二进制。
- [x] 5.2 8077 端到端：真实建链 → 匿名 `view` + 四桶上报 → stats 读数逐项对上；
      跨链接 / 跨成品不串数；撤销后旧链接不再产生新事件；采集关闭时 UI 口径。
- [x] 5.3 OpenSpec 变更归档（+3 Requirement），路线图 §10、工作日志补记。
- [x] 5.4 一次 commit + push，核对远端 CI 全绿。
