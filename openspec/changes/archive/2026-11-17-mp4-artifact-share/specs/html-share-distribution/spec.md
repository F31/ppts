# Spec Delta

## MODIFIED Requirements

### Requirement: 分享链接锚定成品而非项目

分享链接 MUST 绑定到一个既有的 `html_bundle` 或 `mp4` 成品（`artifacts.id`），
MUST NOT 绑定到项目。MUST NOT 存在「链接指向项目最新成品」这种间接绑定——那会让
链接内容随项目编辑变化。

仅绑定成品 ID 不足以满足上一条：成品行的写路径是
`ON CONFLICT (snapshot_hash, format) DO UPDATE SET object_key = ...`，
**同一快照重新导出会原地改写该行**。因此创建链接时 MUST 同时把成品当时指向的
`object_key` 与 `content_hash` 快照进链接行，匿名端 MUST 只读链接行上的快照，
MUST NOT 在交付时回查成品行当前指向的对象。

创建链接时 MUST 校验成品属于同一租户、同一项目，且格式为 `html_bundle` 或 `mp4`；
不满足时 MUST 在创建阶段返回 4xx，MUST NOT 拖到匿名访问时才失败。

同一成品 MAY 存在多条同时有效的链接（不同客户、不同有效期）。

#### Scenario: 创建链接后项目重新导出

- **WHEN** 某成品已有一条有效链接，同一项目又导出一次生成了新的同格式成品
  （`snapshot_hash` 相同则 `artifacts` 行被更新为新 `object_key`）
- **THEN** `html_bundle` 链接读到的 `index.html`、`mp4` 链接读到的媒体字节，
  均与创建链接时读到的**逐字节相同**
- **AND** 响应中的内容标识仍等于创建时该成品的 `content_hash`

#### Scenario: 成品属于其他项目

- **WHEN** 调用方用项目 A 的路径去为项目 B 的成品创建链接
- **THEN** 返回 404，且不创建任何行

#### Scenario: 不可分享格式

- **WHEN** 目标成品的 `format` 不是 `html_bundle` 也不是 `mp4`
- **THEN** 返回 400，错误文案指明格式不支持

## ADDED Requirements

### Requirement: 视频成品通过分享链接直接播放

`mp4` 成品分享链接的根路径 `/s/{token}/` MUST 返回自包含播放页，
媒体流 MUST 从 `/s/{token}/media` 提供，MUST 支持 HTTP Range 以允许拖动与续传。
播放页 MUST NOT 依赖外链脚本或远程资源，MUST 在 `default-src 'none'` 的 CSP 下可播放。

每次媒体请求 MUST 回查链接行的撤销、有效期与口令状态，MUST NOT 重定向到脱离
链接状态的预签名 URL。媒体 MUST 按链接行快照上的 `object_key` 读取，
MUST NOT 回查 `artifacts` 当前指向的对象。

视频链接除入口页与 `media` 外的匿名路径 MUST 一律 404，MUST NOT 借用 token 读取
对象存储或 `html_bundle` 包内条目。入口页与媒体响应 MUST 使用
`Cache-Control: private, no-store`。

#### Scenario: 视频链接直接播放

- **WHEN** 匿名客户端请求有效的 `mp4` 链接根路径
- **THEN** 返回播放页，页面引用同源 `./media`
- **AND** 请求 `/s/{token}/media` 返回 `video/*` 字节并支持 Range

#### Scenario: 撤销后媒体立即失效

- **WHEN** 链接被撤销后，客户端仍带着旧的播放页或已解锁 cookie 请求 `/media`
- **THEN** 返回 404，且不返回任何视频字节

#### Scenario: 越界路径

- **WHEN** 请求 `/s/{token}/../other.mp4` 或其它非 `media` 路径
- **THEN** 返回 404，且不返回对象存储中的任何字节

#### Scenario: 有口令的视频链接

- **WHEN** 视频链接设置了口令且客户端未解锁
- **THEN** 根路径返回口令门，MUST NOT 返回播放页或视频字节
- **AND** 未解锁时请求 `/media` 返回 404

### Requirement: 视频播放次数按播放起点记录

视频分享每次**播放起点**请求 MUST 记录一条审计：action `artifact_share.play`，
`resource_type = artifact`，`resource_id = artifact.id`，metadata 含 `surface`、
`link_id`、`project_id`、`format`。

Range 续传或非零起点请求 MUST NOT 记录。MUST NOT 记录 token、IP、User-Agent。
成品列表的播放次数 MUST 用存储层一次聚合查询得出，MUST NOT 用有上限的事件列表
在应用层计数。

视频链接 MUST NOT 提供逐链接进度统计；对 `mp4` 成品的 `stats` 端点 MUST 返回 400，
MUST NOT 返回空漏斗或一排 0。

#### Scenario: 一次播放只计一次

- **WHEN** 客户端从起点请求视频后继续用 Range 拖动
- **THEN** 只有起点请求产生 `artifact_share.play`，后续 Range 请求不增加播放次数

#### Scenario: 成品卡片播放次数

- **WHEN** 某 `mp4` 成品有累计播放事件
- **THEN** 成品列表的该卡片显示按成品聚合的播放次数

#### Scenario: 视频请求进度统计

- **WHEN** 作者对 `mp4` 成品的分享链接请求 `stats`
- **THEN** 返回 400，错误文案说明该格式没有分链接进度统计
