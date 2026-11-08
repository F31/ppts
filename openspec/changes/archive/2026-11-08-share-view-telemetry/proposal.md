# Proposal: 在线分享观看埋点（V20）

## Why

《动画视频扩展-设计方案 V1.0》§20.8 把「埋点完整性」列为 M6-4 的验收项：首帧曝光、
25/50/75/100% 进度、分享来源上报齐全，隐私模式下关闭。§20.7 更明确写了「分享链接的
观看完成率，是这个功能最该被衡量的指标」。到本变更之前，22 个变更全部归档、139 条
Requirement 落地，唯独这一项没有实现——在线分享只能从 `artifact_share.anonymous_access`
知道「有人打开过」，不知道「看没看完、从哪来」。

开工前有两个必须先解决的冲突：

1. **编号撞车**：初稿把埋点写成 V18，但实现期 G3/G8 已把 V18 用作「切图门禁」、
   V19 用作「页缝延续门禁」，两者都在 CI 里跑、判据与文档互相引用。继续用 V18 会让
   同一个编号指向两套判据。
2. **V14 零外链 vs 上报**：成品包是「断网双击可播」的自包含 HTML，`index.html` 的
   meta CSP 是 `default-src 'none'`，不含 `connect-src`；在线 `/s/` 的响应头 CSP 虽有
   `connect-src 'self'`，但两份 CSP 叠加取**交集**，只改响应头没用。若为了埋点给包里
   加远程资源或放宽 CSP，就破坏了 V14 与隐私承诺。

用户已确认上报边界为「**仅在线分享上报**」：在线 `/s/{token}/` 形态上报，本地包、
隐私模式、离线一律不上报。

## What Changes

- **编号订正**：设计 §20.8 的埋点由 V18 顺延为 **V20**，表格按 CI 实际顺序补齐
  V13b / V18 / V19，§20.7 引用与 M6-4 验收同步。CI 里 V18/V19 两步保持不动。
- **匿名上报端点**：`POST /s/{token}/events`，复用 `audit_events` 写
  `artifact_share.view_event`；事件集合封闭（`view` + 四个进度桶），单请求去重、
  按 token 限流、口令门与链接有效性复用现有语义，不记 token / IP / UA。
- **播放器埋点**：新增 `web/src/sharePlayer/telemetry.ts` 纯判据层（启用判定、同源端点、
  来源提取、进度跨桶、拖动跳过），`main.ts` 只保留排队与一处 `fetch`。
- **CSP**：`indexCSP` 补 `connect-src 'self'`，不放宽任何远程来源，V14 外部引用数仍为 0。
- **门禁**：`web/share-telemetry-check.mjs` 9 项挂进 `npm run build`；
  `scripts/review/telemetry-gate.mjs` 8 条判据进 CI（负例 6 红）；Go 侧 7 组单测。
- **重新生成** `internal/share/assets/app.js`（go:embed 产物，CI 有漂移检查）。

## Capabilities

- `html-share-quality`（新增 3 条 Requirement）：在线启用边界、事件集合完整性、
  隐私与自包含性不被破坏。

## Impact

- 后端：`internal/api/artifact_share_events.go`（新）、`artifact_share_serve.go`、
  `internal/api/artifact_share_events_test.go`（新）、`web/route-inventory.json`。
- 前端/产物：`web/src/sharePlayer/telemetry.ts`（新）、`main.ts`、`web/package.json`、
  `web/share-telemetry-check.mjs`（新）、`internal/share/html.go`、
  `internal/share/assets/app.js`（重新生成）。
- 门禁/CI：`scripts/review/telemetry-gate.mjs`（新）、`.github/workflows/ci.yml`。
- 文档：`docs/动画视频扩展-设计方案-V1.0.md`、`openspec/changes/archive/...`，
  以及项目设计文档原件中的编号同步。

## Out of Scope

- 作者侧观看统计看板与聚合查询 API（事件已落 `audit_events`，可按
  `action = artifact_share.view_event` + `metadata.session/event` 直接查询）。
- MP4 成片、离线 HTML 包、站内预览页的埋点（都不属于在线分享形态）。
- 跨站分析、第三方统计 SDK、用户身份关联、观看者画像。
- 改 CI 里 V18 / V19 两步的编号与判据。
