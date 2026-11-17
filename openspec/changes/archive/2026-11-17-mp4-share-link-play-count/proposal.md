# Proposal: mp4 分享逐链接播放次数

## Why

mp4 分享已经有成品级 `playCount`（成品库卡片），但作者打开分享面板时只能看到
每条链接的创建时间与有效期，无法回答「哪条外发链接真的被打开了」。播放事件本身
已经带 `link_id`，缺的只是一个按链接聚合的出口。

这不是新增采集：不新增表、不新增事件、不采集 token / IP / UA，也不把逐链接
播放次数伪装成观看进度。

## What Changes

- 分享链接列表为 mp4 成品返回每条链接的 `playCount`。
- 存储层按 `metadata.link_id` 一次聚合，不按链接逐条查审计表。
- 聚合失败只降级为 0，不让分享链接的建/列/撤销整体失败。
- HTML 链接继续走既有逐链接观看统计，不混用播放事件。

## Capabilities

- `html-share-distribution`（ADDED 1 条 Requirement）。

## Impact

- `internal/audit` 新增 `CountByMetadata`（SQLite JSON1 / PG jsonb）。
- `internal/api/artifact_share.go` 的链接列表带 `playCount`。
- `web/src/components/ArtifactSharePanel.tsx` 在视频链接行显示该次数。
- 主规格在归档时吸收新增 Requirement。

## Out of Scope

- 播放会话去重：当前口径仍是「播放起点请求次数」，与卡片播放次数一致。
- 逐链接观看进度 / 完播率（仍只对 html_bundle 提供）。
- 时间趋势、CSV 导出、跨成品汇总。
