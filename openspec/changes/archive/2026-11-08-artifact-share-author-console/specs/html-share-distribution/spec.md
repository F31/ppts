# Spec Delta

## ADDED Requirements

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
