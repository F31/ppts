# 任务：HTML 成品的分享落地（M6-3）

> 判据统一写在每条任务里，写完就打勾并附实测值。没有实测值的勾不算数。

## 1. 数据模型与存储

- [x] 1.1 迁移 `0052_artifact_share_links.sql`（PG）：建表 + `UNIQUE(token)` + 两个索引 +
      `ON DELETE CASCADE` + FORCE RLS（`tenant_isolation` + `anon_read` 两条策略）+ GRANT。
      `anon_read` 的 `USING` 必须与 0032 同句式：`revoked = false AND (expires_at IS NULL OR expires_at > now())`
- [x] 1.2 迁移 `sqlite/0019_artifact_share_links.sql`：同结构，无 RLS（单租户 profile），
      时间列用 `TEXT` 并沿用 `db.ParseTime` / `db.Now`
- [x] 1.3 `internal/artifact/share_link.go`：`ShareLink` 结构、`ShareLinkStore` 接口
      （Create / ListByArtifact / Revoke / GetByToken / PasswordHash / TouchAccess）、
      有效性条件抽成**一个常量**由 PG 与 SQLite 共用
- [x] 1.4 PG 实现：写路径走 `tenant.Run`，`GetByToken` 直连 pool（靠 `anon_read`）
- [x] 1.5 SQLite 实现：列清单与 Scan 目标**相邻定义**（本包曾因两者错位静默读错值）
- [x] 1.6 用例：往返、撤销后 `GetByToken` 查不到、过期后查不到、级联删除、
      `password_hash` 不出现在匿名读路径的 Scan 目标里

## 2. 管理端 API

- [ ] 2.1 `POST /projects/{pid}/artifacts/{aid}/shares`：editor 及以上；校验成品属于该租户该项目；
      `format != html_bundle` → 400；口令走既有 `hashPassword(pepper)`；`expiresInDays` 与既有语义一致；
      返回 `token` 与可分享路径 `/s/{token}/`
- [ ] 2.2 `GET /projects/{pid}/artifacts/{aid}/shares`：按创建时间倒序，字段与项目链接保持同形
      （`passwordProtected` 而非哈希）
- [ ] 2.3 `POST /projects/{pid}/artifacts/{aid}/shares/{linkId}/revoke`：只改 `revoked`
- [ ] 2.4 用例：越权（其他项目成品 404，非 editor 403）、非 html_bundle 400、撤销后不可再访问

## 3. 匿名端与内容直出

- [ ] 3.1 `GET /s/{token}` → 302 到 `/s/{token}/`；`GET /s/{token}/` →
      无口令时 `index.html`，有口令且未解锁时口令门页面
- [ ] 3.2 `POST /s/{token}/unlock`：校验口令 → 种 HMAC cookie（绑 linkID + 过期）→ 302 回 `/s/{token}/`；
      口令错误与链接无效返回**同一** 404
- [ ] 3.3 `GET /s/{token}/{path...}`：从 zip 取单条目；越界路径 404；缺失条目 404 且**不回退** index.html
- [ ] 3.4 包缓存：按 `content_hash` 索引 `*zip.Reader`，总字节/条目数双上限 + LRU + singleflight；
      读取超过上限时 500 并记日志
- [ ] 3.5 安全头：CSP / `nosniff` / `no-referrer` / `Cache-Control: private`；不设 `X-Frame-Options`
- [ ] 3.6 审计：`artifact_share.anonymous_access`，只记成功，metadata 不含 token

## 4. 门禁用例

- [ ] 4.1 **锚点**：建链接 → 重新导出 → 旧 token 读到的 `index.html` **逐字节相同**
- [ ] 4.2 **撤销**：撤销后 404，且已持 cookie 的客户端同样 404
- [ ] 4.3 **口令**：无口令直通；有口令未解锁时口令门不含包内字节；错口令与不存在 token 响应体相同
- [ ] 4.4 **路径**：`..`、`%2e%2e%2f`、`/etc/passwd`、`slides/`（目录）全部 404
- [ ] 4.5 **头部**：CSP 存在且不含 `unsafe-eval`，`script-src` 不含 `unsafe-inline`

## 5. 收口

- [ ] 5.1 变更自带 README：表结构、路由表、cookie 语义、缓存与撤销生效延迟（默认 30 秒）、
      体积/内存代价、未做的部分（publication、埋点）
- [ ] 5.2 路线图进度表与计划表更新 M6-3 状态
- [ ] 5.3 `openspec validate --all` 通过后归档，delta 吸收进 `openspec/specs/html-share-distribution`
