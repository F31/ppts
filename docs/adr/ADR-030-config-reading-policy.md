# ADR-030：进程配置集中在 `internal/config`，装配层不再直接读环境

- 状态：已采纳（2026-11-08）
- 关联：`internal/config`、ADR-003（单二进制分发）、`packaging/config.env.example`

## 背景

同一件事在仓库里被实现了四遍：**布尔环境变量哪些取值算真**。

| 位置 | 合法取值 | 非法/未设置 |
|---|---|---|
| `internal/api/security.go` `envBoolValue` | `1/true/yes/on` 与 `0/false/no/off` | fallback |
| `cmd/ppts/worker.go` `envBool` | `1/true/yes/on` 与 `0/false/no/off` | fallback |
| `internal/api/public.go` `anonymousAccessAudited` | 仅 `1`/`true` | false |
| `cmd/ppts/worker.go` `boolEnv` | 仅 `1`/`true`/`yes` | fallback |

后果不是「代码不好看」，而是同一句运维操作在两处表现相反：`PPTS_PPROF=yes` 不开诊断端点（只认 1/true），`PPTS_HSTS=yes` 却会开 HSTS；`PPTS_ORPHAN_DELETE=on` 不删孤儿（`boolEnv` 不认 on），`PPTS_SQLITE_ANALYZE=on` 会开。没有任何注释说明这些差异是有意的，因此也无法判断哪个是 bug。

整数与浮点解析同样有两份（`envIntValue`/`envFloatValue` 与 `envInt`），时间间隔和逗号列表各有一份私有实现。

## 决策

### 1. 一处定义，全部读取走它

新增 `internal/config`，提供 `String`、`Set`、`Bool`、`ParseBool`、`Int`、`Int64`、`Float`、`Duration`、`List`。语义定死：

- 未设置、空串、纯空白 → fallback，**不返回零值**（零值在配置里几乎总是危险默认：0 秒超时、0 并发）；
- 非法值 → fallback，不 panic、不静默降级；
- 布尔只认 `1/true/yes/on` 与 `0/false/no/off`，其余按非法处理；
- `ParseBool` 用于「写错必须拒绝启动」的键（如 `PPTS_S3_USE_SSL`）：没配返回未设置，非法返回错误，不把写错当成 false；
- `Duration` 只认 Go 时长语法（`90s`、`5m`），且非正值回退——这一族配置几乎全是轮询间隔，0 会让 worker 忙转。`PPTS_TEXT_NORM_ENGINE_TTL` 是例外：显式 `0` 关闭缓存，所以它仍用 `String` 自己解析。

### 2. 装配层（`internal/api`、`cmd/`）不得直接读环境

`os.Getenv`/`os.LookupEnv` 在 `internal/api` 与 `cmd/` 的非测试代码中为零。需要读取就调 `config.*`。

`internal/db`、`internal/mail`、`internal/observability`、`internal/pricing`、`internal/gateway`、`internal/integrations/...`、`internal/render` 这类配置构造函数仍然是配置的归属地：它们把一组键收成结构体。读取本身走 `config.*`，布尔语义与装配层相同。一次性脚本和测试可以直接 `os.Getenv`，但凡是 `PPTS_*` 字面量都必须先登记。

### 3. 门禁写进单测，不写在 CI 脚本里

`internal/config/policy_test.go` 遍历仓库并断言：装配层无直接 env 读取；全仓无 `envInt`/`envBool` 一类本地解析器；无 `EqualFold(os.Getenv(...), "true")` 这种一次性布尔判定；无 `config.String(...) == "true"` 这种手写布尔判定。`registry_test.go` 再断言每个被读取的 `PPTS_*` 字面量都在登记表里，且 `packaging/config.env.example` 与 `packaging/config.md` 由 `go generate ./internal/config/` 生成、与登记表一致。`server` / `worker` / `migrate` 启动时对未登记的 `PPTS_*` 打 WARN，日志只含键名。

放在 `go test ./...` 里而不是加一个 CI step：本地跑测试就会红，不必等推送。`lint` 作业本来就会跑 `gofmt` 与 `go vet`，再加一个 job 只是让反馈变慢。

## 影响与已知取舍

- **行为变化**：`PPTS_PPROF=yes`、`PPTS_OBJECT_ALLOW_DELETE=yes`、`PPTS_AUDIT_ANONYMOUS_ACCESS=yes` 现在会按「真」处理（此前只认 `1`/`true`）。`PPTS_SHARE_TELEMETRY=no` 现在会关闭采集（此前只认 `0`/`false`/`off`）。
- **安全相关的开关也按同一套取值生效**：`PPTS_AUTH_DEV_HEADERS`、`PPTS_TRUST_PROXY`、`PPTS_REQUIRE_EMAIL_VERIFIED`、`PPTS_COOKIE_SECURE` 此前只认字面量 `true`，现在 `1/yes/on` 同样生效。这些开关本身都是显式 opt-in（开发头在没有任何真实认证手段时仍然 fail-closed 并给出明确报错），所以风险不在于多认几个「真」写法，而在于**既有部署若把某个开关写成 `=1` 且意图是关闭**——这属于反直觉写法，本 ADR 视为可接受的修正，并在发布说明中提示核对。
- **已迁移**：上述配置构造函数改为 `config.*`。`PPTS_METRICS_TENANT_LABELS=yes` 与 `PPTS_DEBUG_VARS_PUBLIC=on` 现在生效。SMTP 端口和 S3 TLS 开关仍在非法值时拒绝启动。
- **两套缺省**：代码缺省偏向安全（sqlite、开发头关闭、指标拒绝匿名、配音未配置则 worker 不起）。`packaging/config.env.example` 是 PostgreSQL 单机安装模板（密钥 `CHANGE_ME`，TTS 用 `fake` 以便先开机）。全表见 `packaging/config.md`。
- **前端**：前端不读环境变量。上传体积提示仍是 `ImportDialog` 里的 `100 * 1024 * 1024`，与 `PPTS_MAX_UPLOAD_BYTES` 缺省对齐；调大环境变量不会自动改浏览器提示。
