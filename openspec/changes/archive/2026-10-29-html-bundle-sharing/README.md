# 成品分享（M6-3）落地说明

> 变更：`html-bundle-sharing`。对应里程碑 M6-3「成品级 token + 在线链接」。
> 这份文档回答四个问题：**分享的是什么、地址长什么样、凭什么能打开、代价是多少。**

## 1. 分享的是什么

分享的是**某一版成片**，不是项目。

```
项目 ──导出──▶ html_bundle 成品（zip：index.html + 底图/层图 + 音频 + 清单）
                        │
                        └──▶ artifact_share_links（token、口令、有效期、撤销）
                                        │
                                        └──▶ /s/{token}/  在线打开
```

关键点：链接行上存了创建那一刻的 `object_key` 与 `content_hash`。

**为什么不能只存 `artifact_id`**：`artifacts` 的写路径是
`ON CONFLICT (tenant_id, project_id, snapshot_hash, format) DO UPDATE SET object_key = ...`
——同一快照重新导出会**原地改写**该行（这是刻意的：否则用户重导出拿到的还是旧产物）。
如果分享端每次回查 `artifacts`，客户手里的旧链接就会指向新内容。
快照之后链接钉住的是**字节**，不是指针。这条是写完代码被门禁用例逼出来的，见 design ADR-8。

## 2. 地址与动作

### 管理端（需登录，editor 及以上）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/projects/{pid}/artifacts/{aid}/shares` | 建链接。body：`{accessMode?, password?, expiresInDays?}` |
| GET | `/projects/{pid}/artifacts/{aid}/shares` | 列出。按创建时间倒序 |
| POST | `/projects/{pid}/artifacts/{aid}/shares/{linkId}/revoke` | 撤销（立即失效） |

约束：

- 成品必须属于该租户该项目的 `html_bundle`。不满足时：跨项目 → **404**（不是 403，否则「成品存在但不在你的项目」本身可枚举）；格式不对 → **400**。
- 撤销前先校验 `linkId` 属于本合同品，否则可以用成品 A 的路径撤销成品 B 的链接。
- 同一成品可以有多条同时有效的链接（不同客户、不同有效期）。

### 匿名端（无凭证）

| 方法 | 路径 | 行为 |
|---|---|---|
| GET | `/s/{token}` | 302 → `/s/{token}/` |
| GET | `/s/{token}/` | 无口令：`index.html`；有口令且未解锁：口令门 |
| POST | `/s/{token}/unlock` | 校验口令，种 cookie，303 回入口 |
| GET | `/s/{token}/{path...}` | 包内单条目 |

**为什么必须带尾斜杠**：包里所有资源引用都是相对路径（`slides/001.png`、`app.js`、`audio/seg-1.mp3`）。若停在 `/s/{token}`，浏览器会把它们解析到 `/s/` 下，整页 404。这是把静态包搬上 HTTP 最容易踩、也最难看懂的一步。

## 3. 凭什么能打开

### 有效期与撤销

唯一判据只有一处：

```sql
WHERE token = $1 AND revoked = false AND (expires_at IS NULL OR expires_at > now())
```

PG 侧另有 `anon_read` RLS 策略兜底（与迁移 0032 同句式）；SQLite 没有 RLS，靠同一段 `WHERE`。应用层**不再判断一次**——两处判断迟早漂移，而漂移方向几乎总是「库里的行已经撤了，代码还认为有效」。

**撤销是即时的**：没有做设计里提到的 30 秒解析缓存。少一次优化，换「撤销立即生效」这条更强的性质；等数据库真的有压力再加。

### 口令

- 口令哈希是 bcrypt + pepper，与项目分享链接同一套（`hashPassword` / `verifyPassword`）。
- 校验通过后种 `ppts_share=<linkID>.<expUnix>.<hmac>`：
  - `HttpOnly`、`SameSite=Lax`、`Path=/s/{token}`（只对这一条链接的子请求生效）；
  - 只在 TLS 或显式 `PPTS_COOKIE_SECURE=true` 时加 `Secure`。**不信任 `X-Forwarded-Proto`**：客户端可以伪造它，把 cookie 标成 Secure 之后在明文连接上被浏览器丢弃，表现成「口令永远不对」。
  - TTL 2 小时（且不会超过链接自身的 `expires_at`）。
- cookie **不含口令原文**。校验时同时验签名、有效期、以及 cookie 里的 `linkID` 是否就是 token 解析出的那条——`Path` 只是客户端指令，客户端可以把 A 链接的 cookie 发给 B 链接。
- 口令错误返回 **401 + 口令门重渲染（含「口令不正确」）**，而不是与「链接不存在」同一个 404。理由：口令门只在 token 有效时渲染，「链接存在」在页面出现那一刻已经公开，再拿 404 掩盖只会让用户以为链接坏了。未解锁的**子资源**请求仍返回 404——那里没有可以再输一次口令的界面。

### 内容直出与安全头

服务端**不理解清单内容**，只从 zip 里取单个条目原样返回，按扩展名给 `Content-Type`。

- 越界路径（`..`、`%2e%2e%2f`、绝对路径、反斜杠、结尾斜杠、控制字符）**直接拒绝**，不做 `Clean` 后放行——`Clean` 会把 `a/../b` 「修好」成 `b`，正确行为是拒绝。
- 条目缺失**不回退** `index.html`：回退会让 `slides/999.png` 拿到一页 HTML，`<img>` 静默裂图。

响应头：

```
Content-Security-Policy: default-src 'none'; img-src 'self' data:; media-src 'self';
  style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self';
  connect-src 'self'; form-action 'self'; base-uri 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cache-Control: private, max-age=300
```

不设 `X-Frame-Options` / `frame-ancestors`：官网嵌入是既定场景。

## 4. 代价

### 内存

- 单包上限 **64 MiB**（超过直接 500，不截断——截断的 zip 必然解析失败，只会把「包异常」伪装成「包损坏」）。
- 缓存上限 **256 MiB / 8 个包**，按 `content_hash` 索引，LRU 淘汰。
- 缓存键取内容哈希而不是对象键：同一内容的多份对象键只占一份内存。
- 并发首请求用**单飞**合并，避免冷启动时 50 个资源请求把同一个包读 50 遍。
- 条目内容不缓存，每次现解压：单个条目通常几十 KB，deflate 成本远低于多存一份副本。

### 审计

每次成功交付**入口文档**记一条：

```
action        artifact_share.anonymous_access
resource_type artifact_share_link
resource_id   link.id
metadata      {surface, project_id, artifact_id}
```

不记 token / IP / UA（同 0032 的脱敏纪律：token 就是钥匙，写进审计等于让审计库变成比分享有效期更长的二次泄漏面）。

子资源不重复记：一页 20 张切片逐个记会把一次观看放大成几十条，既冲淡信噪比，也给了匿名端一个写入放大杠杆。

与项目分享的 `share_link.anonymous_access` **分开**：「有人看了这个项目」和「有人看了这一版成片」是两条不同的运营事实，混在一起就无法回答「泄露的是哪一版」。

## 5. 门禁

| 用例 | 钉住什么 |
|---|---|
| `TestArtifactShareAnchorsToArtifact` | 同快照重新导出后，旧 token 读到的字节逐字节相同（用例内先断言 `object_key` 真的被改写） |
| `TestArtifactShareLifecycle` | 建 → 302 → 入口 → 条目 → 撤销 → 404；头部齐全 |
| `TestArtifactSharePasswordGate` | 口令门不含包内字节；子资源 404；错口令 401；解锁后 200；伪造签名退回口令门；cookie 无明文口令 |
| `TestArtifactShareRejectsTraversal` | `%2e%2e%2f` / `..%2f` / 目录 / 不存在条目全部非 200 且不泄漏字节 |
| `TestArtifactShareRejectsWrongTargets` | 非 `html_bundle` 400；跨项目 404；跨成品撤销 404 |
| `TestArtifactShareAuditHasNoCredentials` | 审计含 `artifact_id`、不含 token；子资源不重复记 |
| `TestBundleSource*`（5 条） | 条目读取、按内容哈希缓存、LRU 淘汰、16 并发单飞、超限/非法包 |
| `TestShareLink*`（5 条） | 存储往返、撤销/过期关门、`UsableAt` 与 SQL 语义对齐、级联删除、列清单同步 |

## 6. 没做的

- **`/p/{publicId}` 接入**（公开作品广场）。带审核、精选、下架与运营位，且 `publication` 现在的对象模型是 project 而不是 artifact。需要产品先确认「成品算不算作品」。留作 `html-bundle-publication`。
- **埋点 V18**（首帧曝光、25/50/75/100% 进度、分享来源）。归 M6-4。
- **访问配额与独立限流**。沿用现有 `rate_limit` 中间件。
- **字体子集化与体积优化**。归 M6-4。
- **不承诺 DRM**。资产分片不是安全边界；文案是「可控分享」，不是「防下载」。

## 7. 迁移与回滚

- PG：`migrations/0052_artifact_share_links.sql`（新表，无改动既有表）。
- SQLite：`migrations/sqlite/0019_artifact_share_links.sql`。
- 回滚：删表即可，分享路由随之 404；`artifacts` 与既有项目分享链路完全不受影响。
