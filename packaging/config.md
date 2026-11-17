# ppts 配置清单

本文件由 `go generate ./internal/config/` 从登记表生成，不要手改。

未设置、空串、纯空白都视为没配，使用「缺省」列。非法值同样退回缺省，不取零值。
布尔只认 `1/true/yes/on` 与 `0/false/no/off`。时长只认 Go 语法（`90s`、`5m`），0 和负数退回缺省。

两套缺省不要混：

- **代码缺省**是进程什么都不设时的行为，偏向安全：sqlite、开发头关闭、指标拒绝匿名、配音未配置则 worker 不起。
- **安装模板**（`config.env.example`）是 PostgreSQL 单机安装，密钥写 `CHANGE_ME`（install.sh 会换成随机值），TTS 用 `fake` 以便不接供应商也能把服务拉起来。正式配音要改成模型网关或 `siliconflow`。

解析器版本不是环境变量。修订记录里的版本来自 `go.mod` 的 `github.com/F31/go-pptx/v2`，避免报告里的版本和实际链接的库不一致。

| 键 | 类型 | 缺省 | 安装模板 | 说明 |
|---|---|---|---|---|
| `PPTS_DATABASE_URL` | string | `（空）` | `postgres://ppts_app:CHANGE_ME@127.0.0.1:5432/ppts?sslmode=disable` | postgres 为连接串（须为非 owner 的受限账号）；sqlite 为文件路径。sqlite 且留空时用 ~/.ppts/ppts.db。 |
| `PPTS_DB_DRIVER` | string | `（空）` | （空） | sqlite 或 postgres。留空时按连接串推断：postgres:// 走 postgres，其余走 sqlite。 |
| `PPTS_MIGRATE_DATABASE_URL` | string | `（空）` | （空） | 迁移用超级用户连接串。留空则回退 PPTS_DATABASE_URL。 |
| `PPTS_SCHEDULER_DATABASE_URL` | string | `（空）` | `postgres://ppts_scheduler:CHANGE_ME@127.0.0.1:5432/ppts?sslmode=disable` | worker 跨租户领取任务的调度账号。留空则用应用账号；单租户 sqlite 不需要。 |
| `PPTS_TENANT_ID` | string | `（空）` | （空） | worker 只处理这一个租户。留空且为 postgres 时跨租户领取。 |
| `PPTS_HTTP_ADDR` | string | `:8080` | `:8080` | API 监听地址。 |
| `PPTS_WEB_ROOT` | string | `（空）` | （空） | 前端静态目录。留空则使用二进制内嵌的 web/dist。 |
| `PPTS_BYOS_AES_KEY_BASE64` | string | `（空）` | （空） | 租户自带存储凭据的 AES 密钥。留空时模型网关密钥也可兼作此用途。 |
| `PPTS_OBJECT_ALLOW_DELETE` | bool | `false` | `false` | 挂载对象 DELETE 路由。当前没有签发方产出删除令牌，默认关闭。 |
| `PPTS_OBJECT_BACKEND` | string | `local` | `local` | local、s3，或租户策略里的 byos:<credential_id>。 |
| `PPTS_OBJECT_ENCRYPTION_KEY_BASE64` | string | `（空）` | （空） | 对象信封加密密钥（32 字节 base64）。留空则不加密。启用后该租户不能走预签名直读。 |
| `PPTS_OBJECT_ROOT` | string | `（空）` | `/var/lib/ppts/objects` | 本地对象存储根目录。local 后端必填，否则没有存储后端、进程起不来。 |
| `PPTS_OBJECT_SECRET` | string | `（空）` | `CHANGE_ME` | 本地签名链接密钥，32 字节 base64。install.sh 会换成随机值。换密钥会使已签发链接失效。 |
| `PPTS_S3_ACCESS_KEY` | string | `（空）` | （空） | S3 访问密钥。 |
| `PPTS_S3_BUCKET` | string | `（空）` | （空） | S3 桶名。 |
| `PPTS_S3_ENDPOINT` | string | `（空）` | （空） | S3 兼容端点。留空则不注册 s3 后端。 |
| `PPTS_S3_REGION` | string | `（空）` | （空） | S3 区域。 |
| `PPTS_S3_SECRET_KEY` | string | `（空）` | （空） | S3 秘密密钥。 |
| `PPTS_S3_USE_SSL` | bool | `false` | `false` | 访问 S3 端点时使用 TLS。写错的值会拒绝启动，而不是静默改成明文。 |
| `PPTS_AUTH_DEV_HEADERS` | bool | `false` | `false` | 接受未签名的 X-PPTS-Tenant-ID / X-PPTS-User-ID。只用于本机开发。 |
| `PPTS_AUTH_REGISTER_PER_HOUR` | int | `60` | `60` | 单 IP 每小时注册请求上限，含失败请求。非正数退回 60。 |
| `PPTS_COOKIE_SECURE` | bool | `false` | `false` | 强制会话 Cookie 带 Secure。HTTPS 且代理未传协议头时打开。 |
| `PPTS_JWT_SECRET` | string | `（空）` | `CHANGE_ME` | 邮箱登录的 JWT 签名密钥。与 OIDC 都没配、且没开开发头时，服务拒绝启动。 |
| `PPTS_JWT_TTL` | duration | `24h` | `24h` | 会话有效期。非法或缺失时用 24h。 |
| `PPTS_OIDC_CLIENT_ID` | string | `（空）` | （空） | OIDC client id。 |
| `PPTS_OIDC_ISSUER` | string | `（空）` | （空） | OIDC issuer。留空则不启用 OIDC。 |
| `PPTS_OIDC_TENANT_CLAIM` | string | `tenant_id` | `tenant_id` | 从 token 取租户 ID 的 claim。 |
| `PPTS_OIDC_USER_CLAIM` | string | `sub` | `sub` | 从 token 取用户 ID 的 claim。 |
| `PPTS_OPERATOR_USER_IDS` | list | `（空）` | （空） | 逗号分隔的运营商用户 ID，可访问 /admin。 |
| `PPTS_PASSWORD_PEPPER` | string | `（空）` | `CHANGE_ME` | 密码 pepper，32 字节 base64。不入库。更换会使已有密码无法校验。 |
| `PPTS_REGISTER_DAILY_PER_IP` | int | `20` | `20` | 单 IP 每日成功注册上限。0 表示不另加每日限制。 |
| `PPTS_REGISTRATION_ENABLED` | bool | `true` | `true` | 关闭则禁止自助注册。 |
| `PPTS_REGISTRATION_INVITE_CODE` | string | `（空）` | （空） | 非空时注册必须带匹配的邀请码。 |
| `PPTS_REQUIRE_EMAIL_VERIFIED` | bool | `false` | `false` | 未验证邮箱的账号禁止登录。默认只在登录后提示验证。 |
| `PPTS_TRUST_PROXY` | bool | `false` | `false` | 信任 X-Forwarded-For / X-Forwarded-Proto。只在受控反向代理后开启。 |
| `PPTS_MAIL_BACKEND` | string | `（空）` | （空） | smtp、log 或 disabled。留空时：配了 SMTP_HOST 用 smtp，否则不发信。 |
| `PPTS_PUBLIC_BASE_URL` | string | `（空）` | （空） | 邮件里验证链接的公网基址，如 https://app.example.com。留空则按请求推导。 |
| `PPTS_SMTP_FROM` | string | `（空）` | （空） | 发件人。留空则回退 SMTP 用户名。 |
| `PPTS_SMTP_HOST` | string | `（空）` | （空） | SMTP 主机。smtp 后端必填。 |
| `PPTS_SMTP_PASSWORD` | string | `（空）` | （空） | SMTP 密码。 |
| `PPTS_SMTP_PORT` | int | `587` | `587` | SMTP 端口。非法值拒绝启动。 |
| `PPTS_SMTP_TLS` | string | `starttls` | `starttls` | starttls、tls 或 none。 |
| `PPTS_SMTP_USER` | string | `（空）` | （空） | SMTP 用户名。 |
| `PPTS_CSP_CONNECT_EXTRA` | string | `（空）` | （空） | CSP connect-src 的额外来源，空格分隔。 |
| `PPTS_HSTS` | bool | `false` | `false` | 发送 Strict-Transport-Security。只在 TLS 终止之后打开。 |
| `PPTS_SECURITY_HEADERS` | bool | `true` | `true` | 安全响应头总开关。排障可临时关闭。 |
| `PPTS_DEFAULT_GEN_SECONDS_LIMIT` | float | `600` | `600` | 新租户默认免费配音秒数。0 不发放，负数不限量。 |
| `PPTS_MAX_EXPORT_DURATION_MINUTES` | int | `60` | `60` | 单工程成品时长上限（分钟）。0 不限。超限在写出产物前失败。 |
| `PPTS_MAX_PARSE_PAGES` | int | `100` | `100` | 单个 PPT 的页数上限。非正数退回 100。 |
| `PPTS_MAX_UPLOAD_BYTES` | int64 | `104857600` | `104857600` | 上传与解析共用的文件上限，缺省 100 MiB。大于此值的文件前端会拒绝，解析也会拒绝。调大后浏览器里的 100 MB 提示仍是前端常量，需要一并改。 |
| `PPTS_PRICE_BOOK` | string | `（空）` | （空） | 定价表 JSON。留空使用内置占位价。非法 JSON 拒绝加载定价。 |
| `PPTS_DEBUG_VARS_PUBLIC` | bool | `false` | `false` | 允许匿名读 /debug/vars。该端点会带出启动命令行，只用于本机。 |
| `PPTS_METRICS_TENANT_LABELS` | bool | `false` | `false` | 指标是否带租户标签。默认关，避免高基数。 |
| `PPTS_METRICS_TOKEN` | string | `（空）` | （空） | /metrics 与 /debug/vars 的 Bearer。留空则一律拒绝，不会匿名暴露指标。 |
| `PPTS_OTEL_EXPORTER_OTLP_ENDPOINT` | string | `（空）` | （空） | OTLP HTTP 端点。留空则不导出链路。 |
| `PPTS_PPROF` | bool | `false` | `false` | 挂载 /debug/pprof。即使打开也仍要过指标 token。 |
| `PPTS_RATELIMIT_BACKEND` | string | `shared` | `shared` | shared 为跨副本的数据库计数；memory 退回进程内计数（维护窗口）。 |
| `PPTS_WORKER_METRICS_ADDR` | string | `（空）` | （空） | worker 单独监听指标的地址，如 127.0.0.1:8081。留空则 worker 不另开端口。 |
| `PPTS_COSYVOICE_PROMPT_TEXT` | string | `（空）` | （空） | 参考音频对应的文本。 |
| `PPTS_COSYVOICE_PROMPT_WAV` | string | `（空）` | （空） | CosyVoice 零样本参考音频路径。用到该供应商时必填。 |
| `PPTS_NARRATION_TTS_CONCURRENCY` | int | `0` | `0` | 一段配音里的合成并发。0 表示跟随供应商声明；未声明时串行。只能调低，不能高于供应商声明。 |
| `PPTS_TTS_API_KEY` | string | `（空）` | （空） | TTS API 密钥。 |
| `PPTS_TTS_BASE_URL` | string | `（空）` | （空） | TTS 端点。硅基种子缺省 https://api.siliconflow.cn。 |
| `PPTS_TTS_MODEL` | string | `（空）` | （空） | TTS 模型。硅基种子缺省 FunAudioLLM/CosyVoice2-0.5B。 |
| `PPTS_TTS_PROVIDER` | string | `（空）` | `fake` | fake 或 siliconflow。代码缺省为空：没有模型网关时 worker 拒绝启动，避免生产静默出静音。安装模板用 fake，让单机先跑通。正式配音改网关或 siliconflow。 |
| `PPTS_TTS_VOICE` | string | `（空）` | （空） | TTS 音色。 |
| `PPTS_GATEWAY_AES_KEY_BASE64` | string | `（空）` | `CHANGE_ME` | 模型网关与消息服务凭据的 AES-256 密钥，32 字节 base64。留空则这些接口返回 503。 |
| `PPTS_LLM_API_KEY` | string | `（空）` | （空） | 大模型 API 密钥。 |
| `PPTS_LLM_BASE_URL` | string | `（空）` | （空） | 大模型端点。硅基种子缺省 https://api.siliconflow.cn。 |
| `PPTS_LLM_MODEL` | string | `（空）` | （空） | 对话模型。硅基种子缺省 Qwen/Qwen2.5-7B-Instruct。 |
| `PPTS_LLM_PROVIDER` | string | `（空）` | （空） | 留空或 none 表示不启用大模型。当前只接受 siliconflow。 |
| `PPTS_LLM_REQUEST_TIMEOUT` | duration | `180s` | （空） | PPTS_LLM_TIMEOUT 的旧名。两者都设时用新名。 |
| `PPTS_LLM_TIMEOUT` | duration | `180s` | `180s` | 大模型 HTTP 超时。也认旧名 PPTS_LLM_REQUEST_TIMEOUT，新名优先。 |
| `PPTS_LLM_VISION_MODEL` | string | `（空）` | （空） | 视觉模型。留空则不提取视觉锚点。硅基直连时的库内缺省名是 Qwen/Qwen3-VL-8B-Instruct，但不会自动启用。 |
| `PPTS_SCRIPT_DRAFT_CACHE` | bool | `true` | `true` | 讲稿草稿缓存。关掉则每次都重新生成。 |
| `PPTS_SCRIPT_DRAFT_CONCURRENCY` | int | `3` | `3` | 讲稿草稿生成的并发。 |
| `PPTS_TEXT_NORM_CLASSES` | list | `（空）` | （空） | 逗号分隔的类别，如 percent,date,money,unit。留空不注册。 |
| `PPTS_TEXT_NORM_ENABLED` | bool | `false` | `false` | 配音前的文本规范化。默认关，避免改变已有音频。 |
| `PPTS_TEXT_NORM_ENGINE_MAX` | int | `256` | `256` | 每个 worker 缓存的引擎个数。小于等于 0 关闭缓存。 |
| `PPTS_TEXT_NORM_ENGINE_TTL` | duration | `5m` | `5m` | 规范化引擎缓存时间。显式 0 关闭缓存（这是少数允许 0 的键，非法值也会关掉缓存并打日志）。 |
| `PPTS_TEXT_NORM_NUMBERS` | string | `（空）` | （空） | quantity 或 year。留空不注册数值读法。 |
| `PPTS_QUEUE_BACKLOG_INTERVAL` | duration | `30s` | `30s` | 队列最老等待时间的采样间隔。 |
| `PPTS_QUEUE_DEPTH_INTERVAL` | duration | `30s` | `30s` | 队列深度的采样间隔。 |
| `PPTS_REQUIRE_RENDERER` | bool | `false` | `false` | LibreOffice/poppler 不可用时拒绝启动。默认只打日志并继续，页面图会缺失。 |
| `PPTS_TENANT_POLICY_CACHE` | bool | `true` | `true` | 租户策略的进程内缓存。关掉则每次对象操作都读库。 |
| `PPTS_TENANT_POLICY_CACHE_TTL` | duration | `30s` | `30s` | 策略缓存有效期，也是跨进程看见策略变更的上限。同进程 SetPolicy 会立刻失效。 |
| `PPTS_WORKER_CONCURRENCY` | int | `2` | `2` | worker 同时执行的任务数。 |
| `PPTS_WORKER_MAX_ATTEMPTS` | int | `10` | `10` | 单个任务最多尝试次数。 |
| `PPTS_ANIMATION_ASSET_DIR` | string | `（空）` | （空） | 页图资产目录。导出进程写入，渲染容器读取同一目录。留空则预览与动画导出不可用。 |
| `PPTS_ANIMATION_ENABLED` | bool | `（空）` | `false` | 动画总开关。未设置时 sqlite 为开、postgres 为关。安装模板按 postgres 写成 false。 |
| `PPTS_ANIMATION_PREVIEW_BUDGET` | duration | `3s` | `3s` | 单页预览从提交到拿到字节的预算。合法范围 1s–60s，超出会被夹紧。留空用 3s。 |
| `PPTS_ANIMATION_PREVIEW_CLIENT_TIMEOUT` | duration | `70s` | `70s` | 预览 HTTP 客户端超时。必须大于预览预算上限 60s，否则预算还没到客户端先断开。 |
| `PPTS_ANIMATION_PROBE_TIMEOUT` | duration | `3s` | `3s` | 导出对话框探测渲染服务是否在线的超时。要短，否则对话框会卡住。慢机器请加长，不要和 worker 的 30s 混用。 |
| `PPTS_ANIMATION_ROLLOUT` | int | `100` | `100` | 租户没配策略时的灰度百分比 0–100。非法值退回 100，避免写错把所有人的动画关掉。要收口请显式写 0。 |
| `PPTS_ANIMATION_ROLLOUT_SALT` | string | `（空）` | （空） | 灰度分桶盐。写进配置后不要再改，改了等于重新洗牌。 |
| `PPTS_ANIMATION_TIMEOUT_MINUTES` | int | `20` | `20` | 一段动画导出任务的总超时（分钟）。 |
| `PPTS_RENDERER_TIMEOUT_SECONDS` | int | `30` | `30` | worker 调用渲染服务的 HTTP 超时（秒）。这是单次请求，不是整段动画任务。 |
| `PPTS_RENDERER_URL` | string | `（空）` | （空） | 渲染服务地址，如 http://127.0.0.1:8090。留空则不出动画、也不做字体子集。 |
| `PPTS_CHROME` | string | `（空）` | （空） | chrome-headless-shell 或 Chrome 的路径。留空则交给 Remotion 自己找。 |
| `PPTS_FFMPEG` | string | `（空）` | （空） | ffmpeg 可执行文件。留空则在 PATH 里找；找不到时静页/注视页退回 Chrome 合成器。 |
| `PPTS_NODE` | string | `node` | `node` | node 可执行文件。 |
| `PPTS_PDFINFO_BIN` | string | `（空）` | （空） | pdfinfo 路径。留空则在 PATH 里找。 |
| `PPTS_PDFTOPPPM_BIN` | string | `（空）` | （空） | pdftoppm 路径。留空则在 PATH 里找。 |
| `PPTS_RENDERER_SCRIPT` | string | `（空）` | （空） | renderer/render.mjs 的路径。 |
| `PPTS_RENDER_WORK_ROOT` | string | `（空）` | （空） | 渲染临时目录。Windows 上经 WSL 调用 soffice 时必须在 /mnt/<盘符> 下。 |
| `PPTS_SOFFICE_BIN` | string | `（空）` | （空） | LibreOffice soffice 路径。留空则在 PATH 里找。 |
| `PPTS_SUBTITLE_FONT_NAME` | string | `（空）` | （空） | 字幕字体名。留空用内置字体。 |
| `PPTS_AUDIT_ARCHIVE_INTERVAL` | duration | `1h` | `1h` | 审计归档循环的间隔。 |
| `PPTS_AUDIT_RETENTION_DAYS` | int | `365` | `365` | 审计在库里保留的天数。0 或负数表示不按天清理。 |
| `PPTS_JOB_RETENTION_TTL` | duration | `720h` | `720h` | 终态任务保留期，缺省 30 天。到期删除会放开幂等键，同键可以再执行。写 0 不会关闭，只会退回 30 天。 |
| `PPTS_ORPHAN_DELETE` | bool | `false` | `false` | 真的删除无引用对象。默认只写审计。确认候选后再开。 |
| `PPTS_ORPHAN_GRACE` | duration | `24h` | `24h` | 无引用对象在删除或报告前的宽限。 |
| `PPTS_QUOTA_RESERVATION_TTL` | duration | `24h` | `24h` | 仍处于 reserved 的额度预占多久后释放。 |
| `PPTS_RETENTION_INTERVAL` | duration | `1h` | `1h` | 保留清理循环的间隔。 |
| `PPTS_STORAGE_LIFECYCLE_INTERVAL` | duration | `6h` | `6h` | 向对象存储下发生命周期规则的间隔。 |
| `PPTS_UPLOAD_ABANDON_TTL` | duration | `24h` | `24h` | 未完成上传多久后清理。 |
| `PPTS_AUDIT_ANONYMOUS_ACCESS` | bool | `false` | `false` | 把匿名读取公开页记入审计。 |
| `PPTS_SHARE_TELEMETRY` | bool | `true` | `true` | 公开分享页的遥测总开关。 |
| `PPTS_FONT_BASE` | string | `（空）` | — | 字体覆盖测试用的字体目录。 |
| `PPTS_GROWTH_BASELINE` | string | `（空）` | — | 设为 1 时跑任务表增长基线。 |
| `PPTS_GROWTH_CLEANUP` | string | `（空）` | — | 设为 1 时跑保留清理的全量删除演示。 |
| `PPTS_GROWTH_RETENTION_WINDOW` | string | `（空）` | — | 设为 1 时跑 30 天保留窗口演示。 |
| `PPTS_RENDER_FAULT_PAGES` | string | `（空）` | — | 渲染器故障注入的页码列表，只用于自测。 |
| `PPTS_TEST_ADMIN_DATABASE` | string | `（空）` | — | 测试库属主或超级用户连接串，用于 ANALYZE。 |
| `PPTS_TEST_DATABASE` | string | `（空）` | — | PostgreSQL 集成测试的应用账号连接串。未设置时相关测试跳过。 |
| `PPTS_CONFIG` | string | `（空）` | — | gateway-local-tts 等工具读取的 config.env 路径，不是服务进程的配置。 |
| `PPTS_LOCAL_TTS_BASE_URL` | string | `http://127.0.0.1:8000` | — | 本地 TTS 服务地址。 |
| `PPTS_LOCAL_TTS_MODEL` | string | `qwen3-tts-1.7b-customvoice` | — | 本地 TTS 模型名。 |
| `PPTS_LOCAL_TTS_NAME` | string | `local_qwen_tts` | — | 本地 TTS 网关在库里的名字。 |
| `PPTS_LOCAL_TTS_PROVIDER` | string | `openai_compatible` | — | 本地 TTS 的 provider。CosyVoice 运行时填 cosyvoice。 |
| `PPTS_LOCAL_TTS_SET_DEFAULT` | string | `1` | — | 0 表示不抢占现有默认网关。 |
| `PPTS_LOCAL_TTS_VOICE` | string | `vivian` | — | 本地 TTS 音色。 |

