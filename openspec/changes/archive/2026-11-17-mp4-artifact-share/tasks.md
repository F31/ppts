# Tasks: mp4 成品分享规格补正

## 1. 规格

- [x] 1.1 修改「分享链接锚定成品而非项目」：允许 `html_bundle` 与 `mp4`，保留快照与 4xx 语义。
- [x] 1.2 新增「视频成品通过分享链接直接播放」：入口页、`/media`、Range、逐请求回查、no-store。
- [x] 1.3 新增「视频播放次数按播放起点记录」：审计 action / 资源 / 脱敏 / 卡片聚合。
- [x] 1.4 明确视频不提供逐链接进度统计，`stats` 对 mp4 返回 400。

## 2. 代码核对（已上线，仅确认无缺口）

- [x] 2.1 `loadShareableArtifact` 接受 `html_bundle` / `mp4`，非法格式创建时 400。
- [x] 2.2 `serveArtifactShareEntry` 视频只认入口与 `media`，口令门覆盖子请求。
- [x] 2.3 `serveArtifactShareVideo` 从链接快照取对象、透传 Range、播放起点写审计。
- [x] 2.4 `artifactShareStats` 对非 `html_bundle` 返回 400；`GET /artifacts` 聚合 `playCount`。
- [x] 2.5 前端仅对 `mp4` / `html_bundle` 显示分享入口，视频面板不请求进度统计。

## 3. 文档与验证

- [x] 3.1 路线图 §12 移除「mp4 分享规格债」。
- [x] 3.2 `go test ./internal/api` 覆盖视频建链、匿名播放、Range、越界路径、播放计数。
- [x] 3.3 `openspec validate mp4-artifact-share --strict` 通过后归档。
