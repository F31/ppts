# Spec Delta

## ADDED Requirements

### Requirement: 视频分享链接列表返回逐链接播放次数

作者列出 `mp4` 成品的分享链接时，列表 MUST 给出每条链接在审计留存窗口内的
播放次数。该次数 MUST 按播放起点事件（`artifact_share.play`）的 `metadata.link_id`
聚合，MUST 用存储层一次聚合查询完成，MUST NOT 按链接逐条查询审计表。

聚合失败 MAY 使该次列表中的对应次数降级为 0，但 MUST NOT 让链接的创建、列出
或撤销整体失败。响应 MUST NOT 因此返回 token、IP、User-Agent 或单条事件。

逐链接播放次数 MUST NOT 被解释为观看进度或独立会话数；HTML 链接的进度统计
继续走既有 `stats` 端点，视频链接的 `stats` 仍返回 400。

#### Scenario: 两条链接各自计数

- **WHEN** 某视频成品有两条有效链接，链接 A 从起点播放一次并有一次 Range 续传，
  链接 B 从起点播放一次
- **THEN** 分享链接列表返回 A 与 B 的 `playCount` 均为 1
- **AND** Range 续传不重复计数

#### Scenario: 聚合不可用

- **WHEN** 审计存储的按 `link_id` 聚合查询失败
- **THEN** 分享链接列表仍返回 200，次数降级为 0
- **AND** 链接的创建、撤销与列表本身仍可正常使用
