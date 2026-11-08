# html-share-distribution Specification

## Purpose
把已经「能播」的 HTML 成品变成「能发」的成品：一个链接打开的就是**那一版成片**，带口令、有效期与撤销，且分享出去的字节不会随项目后续编辑而改变。这条目的不是「加一个下载入口」——它要解决的是成品作为交付物时唯一不能含糊的属性：**内容稳定**。

## Requirements

### Requirement: 分享链接锚定成品而非项目

分享链接 MUST 绑定到一个既有的 `html_bundle` 成品（`artifacts.id`），MUST NOT 绑定到项目。MUST NOT 存在「链接指向项目最新成品」这种间接绑定——那会让链接内容随项目编辑变化。

仅绑定成品 ID 不足以满足上一条：成品行的写路径是 `ON CONFLICT (snapshot_hash, format) DO UPDATE SET object_key = ...`，**同一快照重新导出会原地改写该行**。因此创建链接时 MUST 同时把成品当时指向的 `object_key` 与 `content_hash` 快照进链接行，匿名端 MUST 只读链接行上的快照，MUST NOT 在交付时回查成品行当前指向的对象。

创建链接时 MUST 校验成品属于同一租户、同一项目，且格式为 `html_bundle`；不满足时 MUST 在创建阶段返回 4xx，MUST NOT 拖到匿名访问时才失败。

同一成品 MAY 存在多条同时有效的链接（不同客户、不同有效期）。

#### Scenario: 创建链接后项目重新导出

- **WHEN** 某成品已有一条有效链接，同一项目又导出一次生成了新的 `html_bundle`（`snapshot_hash` 相同则 `artifacts` 行被更新为新 `object_key`）
- **THEN** 该链接读到的 `index.html` 与创建链接时读到的**逐字节相同**
- **AND** 响应中的内容标识仍等于创建时该成品的 `content_hash`

#### Scenario: 成品属于其他项目

- **WHEN** 调用方用项目 A 的路径去为项目 B 的成品创建链接
- **THEN** 返回 404，且不创建任何行

#### Scenario: 非 html_bundle 成品

- **WHEN** 目标成品的 `format` 不是 `html_bundle`
- **THEN** 返回 400，错误文案指明格式不支持

### Requirement: 匿名访问的有效性只由 token 与链接状态判定

匿名端点 MUST 以「token 命中且未撤销且未过期」作为唯一有效性判据，该判据 MUST 落在一次查询的条件里（与 `anon_read` 策略同一句式），MUST NOT 在应用层重复实现一遍。

不存在、已撤销、已过期三种情况 MUST 返回同一状态码与同一文案。口令错误 MUST 与之一致（返回同一 404），MUST NOT 用 403/401 区分出「链接存在但口令错」。

#### Scenario: 撤销后立即失效

- **WHEN** 一条链接被撤销
- **THEN** 同一 token 的元信息、口令门与全部条目请求均返回 404
- **AND** 已经持有解锁 cookie 的客户端同样返回 404（cookie 只证明口令正确，不证明链接有效）

#### Scenario: 过期

- **WHEN** 链接的 `expires_at` 早于当前时间
- **THEN** 全部匿名请求返回 404

### Requirement: 包内容按条目直出并拒绝越界路径

服务端 MUST 从成品 zip 中按请求路径取出**单个条目**原样返回，MUST 按条目扩展名给出 `Content-Type`，MUST 给出正确的 `Content-Length`。

MUST NOT 列举目录、MUST NOT 在条目缺失时回退到 `index.html`。含 `..` 段、以 `/` 开头、含反斜杠、以 `/` 结尾的路径 MUST 一律 404。

根路径 `/s/{token}` MUST 重定向到 `/s/{token}/`（无尾斜杠会让包内的相对路径解析到错误层级）。

#### Scenario: 正常资源

- **WHEN** 请求 `/s/{token}/slides/001.png`
- **THEN** 返回包内该条目的原始字节，`Content-Type: image/png`

#### Scenario: 越界路径

- **WHEN** 请求 `/s/{token}/../manifest.json` 或 `/s/{token}/%2e%2e%2fmanifest.json`
- **THEN** 返回 404，且响应体不含任何包内字节

#### Scenario: 条目不存在

- **WHEN** 请求包内不存在的路径
- **THEN** 返回 404，不返回 `index.html` 内容

### Requirement: 口令以会话 cookie 覆盖子请求

需要口令的链接，在未解锁时根路径 MUST 返回一个自包含的口令门页面，MUST NOT 含任何包内字节。口令校验通过后 MUST 种一个 `HttpOnly`、`SameSite=Lax`、`Path=/s/{token}` 的签名会话 cookie，使浏览器后续发起的 `<img>`/`<audio>` 子请求同样通过鉴权。

cookie MUST NOT 携带口令本身，MUST 只携带链接标识与过期时间并经 HMAC 签名。cookie 有效期 MUST 短于「链接永不过期」这一默认语义所隐含的长期授权（默认 2 小时）。

#### Scenario: 已解锁后请求条目

- **WHEN** 客户端完成 unlock 并携带会话 cookie 请求 `/s/{token}/slides/001.png`
- **THEN** 返回条目字节，不再要求口令

#### Scenario: 无口令链接

- **WHEN** 链接未设口令
- **THEN** 根路径直接返回 `index.html`，不出现口令门，也不种 cookie

### Requirement: 匿名访问审计可归属到成品链接

每次**成功**交付内容 MUST 记录一条审计：action `artifact_share.anonymous_access`，`resource_type = artifact_share_link`，`resource_id = link.id`，metadata 含 `surface`、`project_id`、`artifact_id`。

MUST NOT 记录 token、IP、User-Agent。失败的请求（口令错、已撤销、已过期）MUST NOT 记录。

#### Scenario: 成功交付后审计

- **WHEN** 一次条目请求成功返回
- **THEN** 审计中出现对应 link ID 的条目
- **AND** 该条目中不含 token 字符串

#### Scenario: 口令错误不记审计

- **WHEN** 客户端提交错误口令
- **THEN** 不产生审计条目

### Requirement: 分享响应带收紧的安全头

所有 `/s/{token}` 下的响应 MUST 带 `Content-Security-Policy`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`。

CSP MUST 禁止外链（`default-src 'none'`），MUST NOT 含 `unsafe-eval`，`script-src` MUST NOT 含 `unsafe-inline`。MUST NOT 设置 `X-Frame-Options` 或 `frame-ancestors`（官网嵌入是既定使用场景）。

包内容响应 MUST 使用 `Cache-Control: private`，禁止共享缓存把一条 token 的响应交给另一条。

#### Scenario: 响应头

- **WHEN** 请求任意 `/s/{token}/...` 资源
- **THEN** 上述四个头均存在，且 CSP 中不含 `unsafe-eval`

### Requirement: 作者可以管理自己成品的分享链接

系统 SHALL 在成品列表提供作者侧入口，用于列出、创建、复制与撤销某一版成片的
在线分享链接；入口 MUST 只出现在可作为成品分享的格式上，且 MUST 复用既有的
受保护端点，不新增匿名写入口。

创建与撤销操作 MUST 要求 editor 及以上角色与项目可见性；成品 MUST 属于路径中的
项目，链接 MUST 属于该成品与该租户，任一不匹配 SHALL 返回 404 而非 403。

#### Scenario: 从成品卡片进入分享面板

- **WHEN** 作者在成品列表打开一个可分享的 HTML 成品卡片并点击「分享」
- **THEN** 面板 SHALL 列出该成品已有的链接，并允许创建新链接、复制链接地址与撤销链接

#### Scenario: 越权的链接 ID

- **WHEN** 请求中的成品不属于该项目，或链接不属于该成品
- **THEN** 系统 SHALL 返回 404，MUST NOT 返回该链接的任何字段或统计数字

### Requirement: 在线分享观看数据可以聚合成作者可读的指标

系统 SHALL 提供只读聚合端点，把 `artifact_share.view_event` 按链接聚合为曝光数、
独立会话数与 25/50/75/100 四个进度桶的独立会话数，并给出完播率所需的分子分母。

聚合 MUST 在存储层用 SQL 完成，MUST NOT 依赖有上限的事件列表再在应用层计数；
计数 MUST 按 `session` 去重，MUST 只计入 `action = artifact_share.view_event` 且
`resource_type = artifact_share_link` 的事件，未知事件名 MUST NOT 计入任何桶。
查询 MUST 按租户隔离，PG 侧 MUST 沿用既有的 `tenant.Run` 上下文。

#### Scenario: 同一会话重复上报

- **WHEN** 一次观看因微批重试落了两条同名事件
- **THEN** 该会话在对应桶里 SHALL 只计一次，聚合结果 MUST NOT 随重试次数增长

#### Scenario: 事件数超过列表上限

- **WHEN** 某链接在窗口内的事件条数远超审计列表的单次上限
- **THEN** 聚合结果 SHALL 仍为全窗口的真实去重会话数，MUST NOT 被静默截断

#### Scenario: 混入别种审计事件

- **WHEN** 同一链接还存在匿名访问、撤销等其它 action 的审计行
- **THEN** 它们 MUST NOT 出现在任何观看指标里

### Requirement: 观看统计界面不得谎报口径

作者侧界面 SHALL 明确标注四项口径：只统计在线 `/s/` 分享观看、离线包与隐私信号
不产生数据、数字只覆盖留存窗口、完播率分母是窗口内独立会话数。

服务端采集总闸关闭时，界面 SHALL 显示「采集已关闭」说明，MUST NOT 把零数据
渲染成一排 0 次观看；请求失败时 SHALL 显示失败原因，MUST NOT 显示为零。
界面 MUST NOT 展示 session、token、IP、UA 或任何单条观看事件。

#### Scenario: 采集总闸关闭

- **WHEN** 服务端 `PPTS_SHARE_TELEMETRY=0` 且聚合结果为空
- **THEN** 面板 SHALL 显示采集已关闭的说明，MUST NOT 显示「0 次观看」之类的统计数字

#### Scenario: 留存窗口

- **WHEN** 审计归档已清理窗口开始之前的事件
- **THEN** 面板 SHALL 按端点返回的窗口标注「最近 N 天」，MUST NOT 声称是累计全量

#### Scenario: 统计请求失败

- **WHEN** 统计端点返回错误或超时
- **THEN** 面板 SHALL 显示加载失败原因，MUST NOT 把数据区渲染成全 0
