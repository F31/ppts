# Tasks: mp4 分享逐链接播放次数

## 1. 存储聚合

- [x] 1.1 `audit.Store.CountByMetadata`：SQLite JSON1 + PG jsonb，限定 action / resource_type / resource_id。
- [x] 1.2 SQLite 隔离用例（成品、动作、租户、缺字段、字段注入）。
- [x] 1.3 PG `-tags=pg` 隔离用例。

## 2. API 与前端

- [x] 2.1 链接列表对 mp4 成品一次聚合，返回每条链接的 `playCount`。
- [x] 2.2 创建链接响应带 `playCount=0`，前端类型同步。
- [x] 2.3 视频链接行显示逐链接播放次数；HTML 的进度统计按钮仍只对 HTML 显示。
- [x] 2.4 分享控制台静态门禁新增逐链接计数断言与变异。

## 3. 验证

- [x] 3.1 `go test ./internal/audit ./internal/api`。
- [x] 3.2 `npm run build`（含 artifact-share / i18n 门禁）。
- [x] 3.3 `openspec validate mp4-share-link-play-count --strict` 后归档。
