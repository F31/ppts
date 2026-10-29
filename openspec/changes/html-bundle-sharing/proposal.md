# 提案：HTML 成品的分享落地（M6-3）

## 为什么做

M6-1 与 M6-2 把成品做到了「解压双击就能播，且与 MP4 同一支片子」。但它的分发方式只有一种：**下载一个 zip**。这在真实场景里几乎等于没有分发——企业微信发 zip、邮件发 zip、官网嵌入 zip 都不成立，移动端连解压都不会做。

方案 §20.7 早就把这条路写死了：在线分享走 `/s/{token}`，复用已有的 `ShareLink` 机制（口令、有效期、撤销、访问记录），但**token 要绑成品 ID 而不是项目 ID**。理由不是洁癖：

> 现在 `ShareLink` 分享的是「项目」，而项目内容会变。客户手里的链接不该随项目编辑悄悄换内容。

这条一旦不成立，`html_bundle` 就失去了它作为交付物的意义——用户下载时看到的是 A 版，一周后点开链接看到的是 B 版，而他并不知道自己看的是哪一版。**成品即快照**，这是 M6-1/M6-2 已经用 `SnapshotHash` + `contentHash` 建立的前提，分享环节必须与之一致。

仓库里已经有的东西（不是要新建）：

- `project_share_links`（迁移 0032）：token、`access_mode`、`password_hash`、`expires_at`、`revoked`、`last_accessed_at`，以及一条 `anon_read` RLS 策略——匿名连接无租户上下文时，只放行「未撤销且未过期」的行。
- `artifact` 表与 `FormatHTMLBundle = "html_bundle"` 产物类型：成品 zip 已经在对象存储里，`content_hash` 就是不可变标识。
- 匿名审计 `share_link.anonymous_access`：刻意不记 token、不记 IP/UA，只记 link ID 与 project ID。

所以本变更的核心不是「加一个下载入口」，而是**把分享的锚点从项目挪到成品**，并解决挪过去之后才出现的新问题：HTML 包不是一个文件，是几十个条目，要按条目直出。

## 目标

1. **成品级 token**：新表 `artifact_share_links`，外键绑 `artifacts(id)`，字段语义与 `project_share_links` 对齐（口令、有效期、撤销、访问时间、访问模式）。同一个成品可以被多条链接分享（不同客户、不同有效期）。
2. **管理端 API**：`POST/GET /projects/{pid}/artifacts/{aid}/shares`、`POST .../{linkId}/revoke`——创建、列出、撤销，要求 editor 及以上，并校验成品属于该租户该项目的 `html_bundle`。
3. **匿名端 `/s/{token}`**：元信息、口令门、包内条目直出。口令校验通过后种短时会话 cookie，让 `<img>`/`<audio>` 这类无法带自定义头的子请求也能通过。
4. **包内容直出**：从对象存储取 zip，按中央目录索引单个条目返回，配 CSP 与内容类型；路径穿越、目录列举、`..`、绝对路径一律拒绝。
5. **可测的锚点语义**：门禁要能证明「重新导出后，旧链接读到的字节不变」。这是本变更唯一不可让渡的验收项。

## 非目标

- **不接 publication（`/p/{publicId}`）**。公开作品广场带审核、精选、下架与运营位，是产品流程而不是分发通道；把成品塞进去要改 `publication` 的对象模型（现在是 project）。留作 `html-bundle-publication`，等广场侧确认「成品算不算作品」再动。
- **不做埋点（V18）**。首帧曝光、25/50/75/100% 进度、分享来源上报全部归 M6-4，本变更只在服务端记「谁访问了哪条链接」（且不含 token/IP/UA）。
- **不做字体子集与体积优化**（M6-4）。
- **不承诺 DRM**。方案 §20.7 已定性：资产分片不是安全边界。文案写「可控分享」，不写「防下载」。
- **不做访问配额与独立限流**。沿用现有 `rate_limit` 中间件；单条链接的 QPS 上限等有真实流量数据再说。

## 与既有变更的关系

- 依赖 `html-share-bundle-static`（M6-1）的 `html_bundle` 产物与包结构：本变更只**读**包，不改包格式。
- 依赖 `html-bundle-animation`（M6-2）的清单 v2：v2 包同样按条目直出，服务端不理解清单内容，故 v1/v2 都能分享。
- 复用 `project` 包里的口令哈希（`hashPassword`/`verifyPassword` + pepper）与审计脱敏纪律，不另起一套。
- 与 `project_share_links` **并存但不合并**：项目链接分享的是「会变的项目」，成品链接分享的是「不变的这一版」。合并成一张表会让 `project_id NOT NULL` 与 `artifact_id NOT NULL` 的语义分叉藏在 NULL 里。

## 关键取舍（细节见 design.md）

**直出方式**：导出时把 zip 解开、逐条目写进对象存储 —— 写放大且要清理；每次请求把整包读进内存 —— 一页十几 MB、并发下直接打爆。选**进程内按 `content_hash` 缓存 zip 索引**：命中时零 IO，未命中一次性读入，按字节上限做 LRU。包是内容寻址的，同一 `content_hash` 的字节永远相同，缓存不需要失效逻辑。

**口令怎么过**：`<img src>` 带不了自定义头，把口令拼进 URL 会进访问日志、Referer 与浏览器历史。选**校验通过后种 HttpOnly 会话 cookie**（HMAC 签名、绑 link ID、短 TTL、`Path=/s/{token}`），子请求靠它鉴权。

**撤销的判据只有一处**：`WHERE revoked=false AND (expires_at IS NULL OR expires_at > now())`，与 0032 的 `anon_read` 策略同一句式。应用层不再判断一次——两处判断迟早会漂移，而漂移的方向通常是「链接已经撤销了但还能打开」。
