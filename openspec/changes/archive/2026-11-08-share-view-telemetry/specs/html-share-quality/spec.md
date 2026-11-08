# Spec Delta

## ADDED Requirements

### Requirement: 观看埋点只在在线分享形态启用

系统 SHALL 在在线成品分享页（`/s/{token}/`）采集观看事件，且 MUST NOT 在离线
自包含成品包、本地静态托管、隐私模式下发出任何上报请求。

启用判据 MUST 同时满足：页面协议为 `http:` 或 `https:`、`location.pathname` 严格
匹配 `/s/[0-9a-f]{32}/`、`navigator.doNotTrack !== '1'`、
`navigator.globalPrivacyControl !== true`、查询参数 `privacy` 不为 `1` 或 `true`。
任一不满足时播放器 MUST 直接关闭整条上报链路，MUST NOT 构造 `fetch` 或注册
`pagehide` 冲刷。

在线分享页的 meta CSP 与响应头 CSP MUST 同时允许 `connect-src 'self'`（两份 CSP
取交集，缺一即静默拦截），且 MUST NOT 因埋点放宽到任何远程来源；成品包的
V14 外部引用数 MUST 保持为 0。

#### Scenario: 在线分享链接打开

- **WHEN** 用户在 `https://host/s/<32位hex token>/` 打开成品分享页且未启用隐私信号
- **THEN** 播放器 SHALL 启用上报，端点为同源相对路径 `<pathname>events`

#### Scenario: 双击本地成品包

- **WHEN** 用户以 `file://` 打开同一份成品包
- **THEN** 播放器 MUST NOT 发出任何上报请求，V16 的「无 404、无 console error」MUST 不被破坏

#### Scenario: 隐私信号与显式开关

- **WHEN** 浏览器发送 DNT / GPC，或 URL 带 `?privacy=1`
- **THEN** 播放器 MUST NOT 上报；服务端 `PPTS_SHARE_TELEMETRY=0` 时端点 MUST 静默返回 204 且零写入

#### Scenario: 门禁静态托管

- **WHEN** 浏览器门禁以 `http://127.0.0.1:8088/` 之类的非 `/s/{token}/` 路径加载成品包
- **THEN** 播放器 MUST NOT 上报，门禁 MUST NOT 因埋点出现额外请求或控制台报错

### Requirement: 观看事件集合完整且不伪造进度

系统 SHALL 采集首帧曝光（`view`）、25% / 50% / 75% / 100% 进度与分享来源。
事件名集合 MUST 是封闭的 `view`、`progress_25`、`progress_50`、`progress_75`、
`progress_100`；服务端 MUST 拒绝集合外的 `kind` / `bucket`，单请求内 MUST 按
`(kind, bucket)` 去重，且一次最多接受 8 条事件、4 KiB 请求体。

首帧曝光 MUST 在第一帧画面渲染到 DOM 之后才发；播放结束时 MUST 补齐 100%，
不依赖渲染循环恰好落在终点帧。拖动进度条跳过的进度桶 MUST 被标记为「不再上报」，
MUST NOT 因跳到 80% 而补报 25/50/75 三条从未播放过的进度。

分享来源 SHALL 只取显式 `utm_source` / `from` 参数或 referrer 的 **host**，
MUST NOT 写完整 URL；越界或含非法字符时 MUST 置空而不是丢弃整批事件。

#### Scenario: 一次完整观看

- **WHEN** 观众从 0 连续播放到结尾
- **THEN** 服务端 SHALL 收到 `view` 与四个进度桶各一条，且 `source` 字段存在

#### Scenario: 拖到 80% 再播放

- **WHEN** 观众把进度条拖到 80% 后继续播放
- **THEN** 25/50/75 MUST NOT 被上报，播完时 SHALL 只补 100%

#### Scenario: 非法事件整单拒绝

- **WHEN** 上报 `bucket: 33`、未知 `kind`、非法 `sessionId` 或空事件数组
- **THEN** 端点 MUST 返回 4xx 且零写入，不得部分落库

### Requirement: 匿名上报不得破坏脱敏与可播放性

匿名事件 SHALL 复用既有审计写入路径，MUST NOT 为此新增匿名可写业务表。
事件 MUST NOT 记录 token、IP、UA 或任何账号标识；`sessionId` SHALL 是一次观看
自生成的随机值。口令保护但未解锁的链接 MUST 与子资源同码拒绝上报；
撤销或过期的链接 MUST NOT 接受事件。

请求 MUST 校验同源 `Origin`、`Content-Type: application/json`，并按 token 限流；
上报失败（无 fetch、CSP 拦截、断网、限流、存储抖动）MUST 静默处理，
MUST NOT 影响播放或产生 console error。

#### Scenario: 脱敏字段核对

- **WHEN** 检查一条 `artifact_share.view_event` 的 Metadata
- **THEN** MUST NOT 出现 `token` / `ip` / `client_ip` / `user_agent` 字段，
  ResourceID MUST 是 link ID 而不是 token

#### Scenario: 未解锁的口令链接

- **WHEN** 观众尚未通过口令门就上报事件
- **THEN** 端点 MUST 返回 404 且零写入

#### Scenario: 上报链路整体失效

- **WHEN** 上报请求跨站、被 CSP 拦截或网络不可用
- **THEN** 播放器 SHALL 继续正常播放，MUST NOT 抛出未捕获异常
