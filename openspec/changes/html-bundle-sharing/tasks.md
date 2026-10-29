# 任务：HTML 成品的分享落地（M6-3）

> 判据统一写在每条任务里，写完就打勾并附实测值。没有实测值的勾不算数。

## 1. 数据模型与存储

- [x] 1.1 迁移 `0052_artifact_share_links.sql`（PG）：建表 + `UNIQUE(token)` + 三个索引 +
      `ON DELETE CASCADE` + FORCE RLS（`tenant_isolation` + `anon_read` 两条策略）+ GRANT。
      `anon_read` 的 `USING` 与 0032 同句式
- [x] 1.2 迁移 `sqlite/0019_artifact_share_links.sql`：同结构，无 RLS（单租户 profile），
      时间列用 `TEXT` 并沿用 `db.ParseTime` / `db.Now`
- [x] 1.3 `internal/artifact/share_link.go`：`ShareLink` / `NewShareLink` / `ShareLinkStore`，
      有效性条件为「两段方言 SQL + 一个 Go 谓词 `UsableAt`」（见 design ADR-5 的修正）
- [x] 1.4 PG 实现：写路径走 `tenant.Run`，`GetByToken` 直连 pool（靠 `anon_read`）
- [x] 1.5 SQLite 实现：列清单与 Scan 目标相邻定义
- [x] 1.6 用例 5 条全绿：往返、撤销/过期关门、`UsableAt` 与 SQL 的 4 种组合对齐、
      成品删除级联、PG/SQLite 列清单逐字同步且不含 `password_hash`
- [x] 1.7 **补列**：`object_key` / `content_hash` 快照。首版没有这两列，
      `TestArtifactShareAnchorsToArtifact` 第一次跑就红——`artifacts` 的 upsert 会原地改写
      `object_key`，只绑 `artifact_id` 挡不住内容漂移（design ADR-8）

## 2. 管理端 API

- [x] 2.1 `POST /projects/{pid}/artifacts/{aid}/shares`：editor 及以上；成品须属于该租户该项目；
      `format != html_bundle` → 400；口令走既有 `hashPassword(pepper)`
- [x] 2.2 `GET .../shares`：按创建时间倒序，字段与项目链接同形（`passwordProtected` 而非哈希）
- [x] 2.3 `POST .../shares/{linkId}/revoke`：先校验 linkID 属于本成品，再撤销
      （`store.Revoke` 只按 `(id, tenant)` 定位，漏了这步就是跨成品越权撤销）
- [x] 2.4 用例：非 `html_bundle` → 400；跨项目成品 → 404；跨成品撤销 → 404

## 3. 匿名端与内容直出

- [x] 3.1 `GET /s/{token}` → 302 到 `/s/{token}/`；`GET /s/{token}/` → `index.html` 或口令门。
      重定向不是洁癖：包内资源全是相对路径，停在无斜杠形式会让整页 404
- [x] 3.2 `POST /s/{token}/unlock`：校验口令 → 种 HMAC cookie（绑 linkID + 过期 + `Path=/s/{token}`）
      → 303 回入口。口令错误返回 401 并重渲染口令门——口令门只在 token 有效时出现，
      「链接存在」在那一刻已经公开，再用 404 掩盖「口令错」只会让用户以为链接坏了
- [x] 3.3 `GET /s/{token}/{path...}`：从 zip 取单条目；越界 404；缺失 404 且不回退 `index.html`
- [x] 3.4 `internal/share/serve.go`：按 `content_hash` 索引 zip，单包 64MiB / 缓存 256MiB / 8 包，
      LRU + 单飞。用例验证：同内容 5 次读只取 1 次包、16 并发只取 1 次、LRU 淘汰后重取
- [x] 3.5 安全头：CSP / `nosniff` / `no-referrer` / `Cache-Control: private`；不设 `X-Frame-Options`
- [x] 3.6 审计 `artifact_share.anonymous_access`：只记入口文档，metadata 含 `artifact_id`、不含 token

## 4. 门禁用例

- [x] 4.1 **锚点**：建链接 → 同快照重新导出（用例内先断言 `object_key` 真的被改写）→
      旧 token 读到的 `index.html` **逐字节相同**。首版红，补 `object_key` 快照后绿
- [x] 4.2 **撤销**：撤销后 404；已持 cookie 的客户端同样 404
- [x] 4.3 **口令**：无口令直通；有口令未解锁时口令门不含包内字节；错误口令 401 且提示；
      伪造签名的 cookie 退回口令门；cookie 里不含口令原文
- [x] 4.4 **路径**：`%2e%2e%2f`、`..%2f`、`slides/`（目录）、不存在条目全部非 200 且不泄漏字节
- [x] 4.5 **头部**：CSP 含 `default-src 'none'`、`nosniff`、`no-referrer` 均存在

## 5. 收口

- [ ] 5.1 变更自带 README：表结构、路由表、cookie 语义、撤销生效延迟（本实现是**即时**，
      没有做设计里提到的 30 秒解析缓存）、内存代价、未做的部分（publication、埋点）
- [ ] 5.2 路线图进度表与计划表更新 M6-3 状态
- [ ] 5.3 `openspec validate --all` 通过后归档，delta 吸收进 `openspec/specs/html-share-distribution`
