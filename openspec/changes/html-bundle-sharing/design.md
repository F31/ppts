# 设计：HTML 成品的分享落地（M6-3）

## 1. 分层与职责

```
管理端（需登录）                        匿名端（无凭证）
POST /projects/{pid}/artifacts/{aid}/shares    GET  /s/{token}          → 302 → /s/{token}/
GET  /projects/{pid}/artifacts/{aid}/shares    GET  /s/{token}/         → index.html（或口令门）
POST .../shares/{linkId}/revoke                POST /s/{token}/unlock   → 校验口令，种 cookie，302
                                               GET  /s/{token}/{path...} → 包内单条目
        │                                        │
        ▼                                        ▼
artifact_share_links（绑 artifacts.id）      bundle cache（按 content_hash 索引 zip）
        │                                        │
        └──────────────► objectstore.Get(object_key) ◄──────────────┘
                              （成品 zip，只读）
```

服务端**不理解清单内容**。它只做三件事：证明这条 token 此刻有效、把 zip 里某个条目原样吐出来、记一笔审计。这样 M6-2 的清单版本升级、M6-4 的字集/编码变化都不需要动分享链路。

## 2. 数据契约

### 2.1 表 `artifact_share_links`

| 列 | 类型（PG / SQLite） | 语义 |
|---|---|---|
| `id` | uuid / TEXT | 主键 |
| `tenant_id` | uuid / TEXT | 租户隔离（与 0032 同一套 RLS 句式） |
| `artifact_id` | uuid FK → `artifacts(id)` ON DELETE CASCADE / TEXT | **锚点**：成品，不是项目 |
| `project_id` | uuid / TEXT | 冗余列，只为「按项目列出分享」与审计可读；**不参与内容定位** |
| `object_key` | text | **创建链接那一刻**成品指向的对象键（快照）。见 ADR-8 |
| `content_hash` | text | 同时刻的内容哈希（快照），用作 bundle 缓存的键 |
| `token` | text UNIQUE | 32 位十六进制，不可反推 |
| `access_mode` | text CHECK | `view_only` / `view_and_comment`（沿用既有枚举，不新增取值） |
| `password_hash` | text NULL | bcrypt + pepper；NULL = 无口令 |
| `password_protected` | bool | 冗余布尔位：匿名查询**不 SELECT 哈希**，只看这一位决定要不要开口令门 |
| `expires_at` | timestamptz NULL / TEXT NULL | NULL = 永不过期 |
| `revoked` | bool | 撤销后立即失效 |
| `created_by` | text | 创建者 |
| `created_at` | timestamptz / TEXT | |
| `last_accessed_at` | timestamptz NULL / TEXT NULL | NULL = 从未被访问 |

`password_protected` 与 `password_hash` 并存看起来冗余，但它是 0032 已经付过代价的教训：匿名链路不能把哈希读出来（哪怕只是读出来再丢弃），否则任何一次日志/序列化失误都会把口令哈希送到公网侧。

### 2.2 索引与约束

- `UNIQUE (token)`：token 是钥匙，必须全局唯一。
- `INDEX (artifact_id)`：管理端按成品列链接。
- `INDEX (project_id)`：成品库按项目聚合时用。
- `artifacts(id)` 上的 `ON DELETE CASCADE`：成品删除后链接自动消失，不需要应用层清理协程。

## 3. ADR

### ADR-1：新表 vs 给 `project_share_links` 加 `artifact_id`

| 方案 | 做法 | 代价 |
|---|---|---|
| A 新表 `artifact_share_links` | 独立表，`artifact_id NOT NULL` | 一份重复的列定义与两套 CRUD |
| B 给既有表加可空 `artifact_id` | `project_id`、`artifact_id` 二选一非空 | `CHECK ((project_id IS NULL) <> (artifact_id IS NULL))` + 所有既有查询要补条件；`anon_read` 策略要同时覆盖两种语义；`GetShareLinkByToken` 的返回结构变成联合体 |
| C 复用 project 链接，指向「最新成品」 | 不改表 | 直接违背本变更的目的：内容随项目变 |

**选 A。** B 的代价不在迁移，而在**读路径**：现存的 `sharedManifest`、`listShareLinks`、`revokeShareLink` 全部假设「一条链接属于一个项目」。加一个可空列之后，每个读路径都要先判断类型再分支，而这个分支永远不会被测试覆盖满——「一条 artifact 链接被当成 project 链接处理」会安静地返回错误内容，而不是报错。A 多写一份代码，但两条路径各自只有一种语义。

### ADR-2：token 用不透明随机串，不用 JWT

撤销必须**立即**生效。JWT 是无状态的，撤销要么等过期，要么维护一张黑名单——而黑名单本身就是一张「token 状态表」，等于绕一圈回到数据库。

因此 token 是 32 位十六进制随机串（`uuid` 去横线，与 0032 同形），有效性完全由一次索引查询决定。查询条件里同时承载「未撤销」与「未过期」，见 ADR-5。

代价是每次文件请求多一次 DB 查询。这个代价可以消掉：文件请求先验签 cookie（见 ADR-4），cookie 里带 `linkID` 与过期时间；只有在**没有 cookie** 时才回查数据库。

### ADR-3：包内容怎么直出

| 方案 | 做法 | 问题 |
|---|---|---|
| A 导出时解包写对象存储 | 每个条目一个对象键 | 写放大（几十个 PUT/包）、删除要级联、对象键数量爆炸；且与「成品是一个 zip」的既有语义冲突 |
| B 每请求整包读进内存 | `Get` → `zip.NewReader` | 20 页包 ~2.5MB，并发 50 就是 125MB 瞬时分配；首屏 `index.html` 也要等整包下载完 |
| C 解到临时目录 | 本地 `os.MkdirTemp` | 多实例部署下缓存不共享、要清理、要防磁盘打满；且本地存储之外的 objectstore 后端仍要下载整包 |
| **D 进程内按 `content_hash` 缓存 zip 索引** | 首次请求读入整包字节并建 `*zip.Reader`，之后按条目直出 | 内存上限需自管 |

**选 D。** 关键前提是成品**内容寻址**：`artifact.content_hash` 相同即字节相同，所以缓存**不需要失效逻辑**，只需要淘汰。实现要点：

- 缓存键用 `content_hash`（不是 `object_key`），这样同一内容多份对象键也只占一份内存。
- 总字节上限（默认 64 MiB）+ 条目数上限（默认 8 个包），LRU 淘汰。
- 加载用 singleflight 合并并发首请求，避免 50 个请求同时把同一个包读 50 遍。
- 条目内容**不缓存**，每次从 `zip.File.Open()` 现解压：单个条目通常几十 KB，deflate 解压成本远低于多存一份副本的内存代价。

兜底：`ObjectStore.Get` 返回的 `io.ReadCloser` 不可 seek，所以必须先读成 `[]byte` 再建 `zip.Reader`。读取时设上限（默认 64 MiB），超限直接 500 并记日志——一个 2GB 的「成品包」不是包，是攻击。

### ADR-4：口令在子请求上的传递

`index.html` 里的 `<img src="slides/001.png">`、`<audio src="audio/001.mp3">` 由浏览器自己发起，带不了 `X-Share-Password`。

| 方案 | 问题 |
|---|---|
| 口令拼进 URL query | 进访问日志、Referer、浏览器历史、CDN 日志；一次泄漏等于永久泄漏 |
| 用 `Authorization` 头 | `<img>`/`<audio>` 无法设置 |
| 把口令写进页面，由 JS 用 XHR 取资源再转 blob | 要重写播放器的全部资源加载逻辑，且 blob URL 会让 `<audio>` 的 seek 变复杂 |
| **HttpOnly 会话 cookie** | 需要服务端签发与验签 |

**选 cookie。** 细节：

- 校验成功后种 `ppts_share=<linkID>.<exp>.<hmac>`，HMAC key 复用服务端 `PasswordPepper`（已有配置，不新增密钥管理）。
- `HttpOnly`、`SameSite=Lax`、`Path=/s/{token}`（只对这条链接的子请求生效，多链接互不干扰）、HTTPS 下加 `Secure`。
- TTL 默认 2 小时：够看完一场讲解，又不至于让「撤销链接」在已解锁的浏览器里长期失灵。
- **cookie 不代替 token**：cookie 只证明「这个人知道这条链接的口令」，有效性仍要回查数据库（撤销/过期仍然立即生效）。所以每个文件请求是「验 cookie + 查链接」，只是查链接可以走短 TTL 的进程内缓存（默认 30 秒）。**撤销的生效延迟上限 = 这个缓存 TTL**，写进 README，不藏着。

### ADR-5：撤销与有效期的判定只有一处

`WHERE token=$1 AND revoked=false AND (expires_at IS NULL OR expires_at > now())`。

不在应用层再写一遍 `if link.Revoked || link.ExpiresAt.Before(now)`。理由：两处判断迟早漂移，而漂移方向几乎总是「库里的行已经撤了，但代码里那份副本还认为有效」。0032 的 `anon_read` 策略用的就是这个句式，本变更照抄，让 PG 的 RLS 与应用查询成为同一条判据。

SQLite profile 没有 RLS，靠同一段 `WHERE` 保证语义一致。落地时发现**两边不能共用同一段 SQL 字面量**：PG 用 `now()`，SQLite 的 `datetime('now')` 是 `2006-01-02 15:04:05` 而本仓库存的是 `...T...Z`，字符串直比会错序。因此实际做法是「两段方言 SQL + 一个 Go 谓词 `UsableAt`」，再用 `TestShareLinkUsableAtMatchesSQL` 拿同一张矩阵断言三者判断一致——比共用一段字符串更能防住「本地全绿、线上放行已撤销链接」。

### ADR-6：审计

新建 action `artifact_share.anonymous_access`，`resourceType = "artifact_share_link"`，`resourceID = link.ID`，`metadata = {surface, project_id, artifact_id}`。

**不记 token、不记 IP、不记 UA**——与 0032 同一条纪律，理由也一样：token 就是钥匙，写进审计等于让审计库变成比分享有效期更长的二次泄漏面。

不复用 `share_link.anonymous_access` 的原因：那个 action 的 `resourceType` 是 `share_link`，混用会让「按资源类型统计分享访问」把两种完全不同的东西加在一起——项目链接被打开是「有人看了这个项目」，成品链接被打开是「有人看了这一版成片」，运营指标不是一回事。

只记**成功**访问（含口令校验通过后的首次解锁），失败不记：匿名端点失败无成本，记失败等于给扫描方一个存储放大杠杆。

### ADR-7：CSP 用 `'self'` 而不是方案里写的 `sha256-<hash>`

方案 §20.7 写的是 `script-src 'self' 'sha256-<hash>'`。落地时取 `script-src 'self'`：

- 包里所有脚本都是我们自己生成、自己打包、逐字节可控的条目（`app.js`、`manifest.js`），不存在第三方脚本；`'self'` 与逐脚本哈希在这个前提下强度相同。
- 逐脚本哈希要求打包器在生成 `index.html` 时回填哈希，属于改包格式（M6-1/M6-2 的产物），与本变更「只读包」的边界冲突。
- 真要防的是**包内被注入脚本**，而那需要成品包本身被篡改——那不是 CSP 的防线，是 `content_hash` 的防线。

完整响应头：

```
Content-Security-Policy: default-src 'none'; img-src 'self' data:; media-src 'self';
  style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; connect-src 'self'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cache-Control: private, max-age=300
```

- `'unsafe-inline'` 只给 `style-src`：播放器用内联样式做定位（M6-2 的层定位就是 inline style），禁掉会整页错位。
- 不设 `X-Frame-Options`、不设 `frame-ancestors`：方案明确「官网嵌入」是使用场景之一。
- `Referrer-Policy: no-referrer`：防止链接出现在外链站点的 Referer 里。
- `Cache-Control: private, max-age=300`：允许浏览器缓存（同一 token 的包内容不变），但禁止 CDN/代理共享缓存——否则一条 token 的响应可能被缓存给另一个 token 的请求。

### ADR-8：链接必须快照 `object_key`，只绑 `artifact_id` 不够

这条是**写完代码后被门禁逼出来的**，不是设计时想到的。

方案 §20.7 的说法是「token 绑成品 ID，天然对齐 `SnapshotHash` 的版本不可变语义」。前提是「成品行不可变」。核对 `internal/artifact` 的写路径：

```sql
INSERT INTO artifacts ... ON CONFLICT (tenant_id, project_id, snapshot_hash, format)
DO UPDATE SET object_key = EXCLUDED.object_key, content_hash = EXCLUDED.content_hash, ...
```

**成品行是可变的**——同一快照重新导出会原地改写 `object_key`，注释里写得很清楚：「若继续保留旧 object_key，用户重导出下载到的仍是旧产物」。于是「绑 `artifact_id`」只保证了绑到同一行，没保证绑到同一份字节：`TestArtifactShareAnchorsToArtifact` 第一次跑就红了，旧链接读到了新版内容。

修法不是去改 `artifacts` 的 upsert（那会破坏「重导出应拿到新产物」这个正确行为），而是在**创建链接时把 `object_key` 与 `content_hash` 拷进链接行**。之后：

- 分享端只读链接行，不再回查 `artifacts`；
- 旧链接钉住的是字节，不是指针；
- 代价是成品行被删除后旧包可能仍在存储里（retention 会清理），此时链接 404——这是**诚实**的失败，比悄悄换内容好。

这条同时也是「门禁先写、实现后补」的价值样本：如果只验「链接能打开」，这个缺陷会一路带到线上，而且表现为「客户说链接里的内容变了」这种极难复现的问题。

## 4. 边界与失败

| 情况 | 行为 |
|---|---|
| token 不存在 / 已撤销 / 已过期 | 404，文案统一 `share link not found`，不区分 |
| 口令错误 | 与上一条**同一** 404（在 unlock 端点返回 403 会泄漏「链接存在」） |
| 成品已删除 | 链接行被级联删除 → 上面第一条 |
| 成品不是 `html_bundle` | 创建链接时 400；不拖到匿名访问时才失败 |
| 包里没有该条目 | 404，且**不回退**到 index.html（回退会让 `slides/999.png` 拿到一页 HTML，`<img>` 静默裂图） |
| 条目是目录（以 `/` 结尾） | 404；不做目录列举 |
| 路径含 `..` / 以 `/` 开头 / 含反斜杠 | 404；与 `checkResourceKey` 同一条纪律，不用 `filepath.Clean` 兜底（Clean 会「修好」攻击路径而不是拒绝它） |
| 包超过字节上限 | 500 + 日志；不截断 |
| 对象存储读失败 | 502（上游故障与链接无效必须可区分，否则排查全靠猜） |

## 5. 门禁

「链接内容不随项目编辑变化」必须可测，否则这条设计只是文案：

1. **锚点门禁**：建链接 → 记下读到的 `index.html` 字节与 `content_hash` → 用同一项目重新导出一份新成品（`artifacts` 行被 `ON CONFLICT` 更新为新 `object_key`）→ 旧 token 读到的字节**逐字节相同**。
2. **撤销门禁**：撤销后同一 token 404；已解锁的 cookie 也 404（证明 cookie 不代替数据库判定）。
3. **口令门禁**：无口令链接直接 200；有口令链接未带 cookie 返回口令门（不含任何包内容）；错误口令与不存在 token 的响应体**完全相同**。
4. **路径门禁**：`../`、`%2e%2e%2f`、绝对路径、目录路径全部 404，且不返回任何条目内容。
5. **头部门禁**：响应带 CSP、`nosniff`、`no-referrer`；CSP 里不含 `unsafe-eval`，`script-src` 不含 `unsafe-inline`。
