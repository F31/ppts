# Proposal: mp4 成品分享规格补正

## Why

`1.0.0-preview.41` 已上线 mp4 成品在线分享：作者可以从成品卡片建链接，匿名端
`/s/{token}/` 直接播放，`/s/{token}/media` 支持 Range，成品卡片显示播放次数。
但主规格 `openspec/specs/html-share-distribution/spec.md` 仍写着「格式必须是
`html_bundle`，否则创建阶段返回 400」，与已交付代码直接冲突。

这不是新增功能，而是把已实现并已部署的行为补回规格。若继续放任，后续实现者
会按规格把 mp4 分享当越界改回去；评审也无法从主规格判断视频分享的边界。

## What Changes

- **放宽分享锚点格式**：`html_bundle` 或 `mp4` 均可创建链接；仍绑定成品而不是项目，
  仍快照 `object_key` / `content_hash`，非法格式仍在创建阶段返回 4xx。
- **新增视频直出 Requirement**：入口页自包含、媒体走 `/s/{token}/media` 并支持 Range、
  每次请求回查链接状态、不签发脱离撤销状态的预签名 URL、除入口与媒体外一律 404。
- **新增视频播放计数 Requirement**：播放起点记 `artifact_share.play`，按成品聚合成
  卡片播放次数；Range 续传与拖动不重复计；不记 token / IP / UA。
- **明确统计边界**：逐链接进度漏斗与 `stats` 端点仍只适用于 `html_bundle`；
  视频请求返回 400，而不是显示一排 0 误导作者。

## Capabilities

- `html-share-distribution`（MODIFIED 1 条、ADDED 2 条 Requirement）。

## Impact

- 规格：`openspec/specs/html-share-distribution/spec.md`（本变更归档时吸收）。
- 代码已存在，不需要改动：`internal/api/artifact_share.go`、
  `internal/api/artifact_share_serve.go`、`internal/api/artifact_share_stats.go`、
  `internal/api/artifact.go`、`web/src/components/ArtifactSharePanel.tsx`、
  `web/src/pages/Library.tsx`。
- 路线图：`openspec/roadmap/动画视频扩展-V1.0-路线图.md` §12 移除该规格债。

## Out of Scope

- 视频逐链接播放统计、观看进度、完播率。
- 播放会话去重与按天趋势。
- 移动端真机验收与视频码率调整。
- 修改 `html-share-distribution` 能力标识；能力名保留历史名称，避免动已归档引用。
