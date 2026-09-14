# PPT 自动讲解工具

将 PPTX 一键转化为"专业讲解"：AI 讲稿 + 正式 TTS 配音 + 页面同步字幕，产出可试听、可修改、可导出的讲解工程。

- 技术基线：《PPT自动讲解工具-技术方案-V4.0.md》（`docs/`）
- 开发计划：`docs/PPT自动讲解工具-开发计划-V1.0.md`
- 组件依赖：go-pptx v1.0.x（`github.com/F31/go-pptx`；开发期经 `go.work` 指向本地产库）
- go-pptx 增强/缺陷走统一跟踪计划：`docs/go-pptx特性与bug跟踪计划.md`

## 目录结构

```text
cmd/api           云端 API 入口
cmd/worker        任务 worker（租约执行与步骤恢复）
cmd/local-engine  桌面 Go sidecar（G4）
internal/project  源文件版本/页面/元素/导入报告 + go-pptx 读适配器
internal/narration 讲稿/分段/来源锚点/术语/审核
internal/pipeline 任务状态/重试/租约/取消
internal/media    音轨/字幕/时间轴/导出
internal/tenant   身份/角色/策略/RLS
internal/usage    额度/结算/账本/成本
internal/integrations  LLM/TTS/渲染/存储适配（含 objectstore）
proto/ppts/v1     Connect 契约（8 个服务）
migrations        SQL 迁移（expand→migrate→contract）
web/              前端 TypeScript 工作区（Vite + React）
testdata          兼容性语料/评测集
docs/             技术方案/开发计划/ADR/跟踪计划
```

## 开发命令

```bash
GOWORK=off go build ./...        # BUG-001 修复前使用发布版 go-pptx v1.0.1
GOWORK=off go vet ./...
GOWORK=off go test ./...

# 含 PostgreSQL 的测试（共享测试库，必须串行 -p 1）
PPTS_TEST_DATABASE='postgres://.../ppts_test' GOWORK=off go test -tags=pg -p 1 ./... -count=1

cd web && npm ci && npm run build
```

当前 Connect API 已提供 Project/Script/Narration/Playback/Export/Upload 主链路：
- `ProjectService`：`Create/Get/List/Archive/GetSlides`；
- `UploadService`：`CreateUpload/CompleteUpload/AbortUpload`（授权直传：分配受限对象键与预签名写链接，完成时校验大小/哈希/租户所有权后才创建源版本并入队解析任务）；
- `ScriptService`：`Get/Update/Approve/Lock/GenerateDraft`（原文讲稿生成）；`NarrationService.CreateGeneration`；`ExportService`（`CreateExport/GetArtifact/CreateDownload`，格式 `Web 讲解工程`(zip：timeline+字幕+音频片段)/`MP4`(需 page_png_keys)/`SRT`/`VTT`）；`PlaybackService.GetNarration/GetManifest`（GetNarration 发现最近成功配音时间轴，并在解析阶段已渲染页面时返回按 timeline 页序对齐的 `page_png_keys`；渲染器不可用/失败时降级为音频+字幕，GetManifest 允许无页面图）。

页面图渲染（G0-2/G1-6）：worker 解析任务在读完源 PPTX 后调用 `internal/integrations/render`（LibreOffice→PDF→poppler→PNG）渲染全页，写入 `{tenant}/{project}/src-NN/render/page-NNNN.png`，并以 `render/pages.json` 记录页序/slideId 清单（登记在解析任务 `pages` 步骤的 `result_ref`）；渲染是可选增强，失败只记步骤失败、不影响解析成功。

Web 真实链路：登录（正式 OIDC，未配置时开发身份入口）→ 控制台（首页/讲解项目/任务中心/系统设置）→ 直传 → 解析（可选渲染页面图）→ 展示真实页面 rail → 选择讲稿模式（原文/润色/AI 生成）→ 逐页编辑并保存到真实 `UpdateScript`（`expected_revision` 乐观并发，冲突时后端返回 latest、前端加载最新版并提示）→ 生成配音 → 拉取真实播放 manifest（音频+字幕+页面图）→ 导出下载（Web 讲解工程/MP4/SRT/VTT）；`local://` 预签名链接（上传、播放、导出）统一由 API 的 `/ppts/object/{key}` 端点重写服务，S3 后端返回原生预签名 URL。真实链路的 HTTP 端到端验收测试见 `internal/api/e2e_test.go`（`-tags=pg`，真实 PG + 本地存储 + 真实 worker，TTS 用 fake）。

## 本地服务

API 依赖已应用迁移的 PostgreSQL。生产身份可配置 OIDC bearer token 校验：设置
`PPTS_OIDC_ISSUER`、`PPTS_OIDC_CLIENT_ID` 后，API 通过 issuer discovery/JWKS 验签，默认从
`tenant_id` 与 `sub` claims 推导租户和用户；可用 `PPTS_OIDC_TENANT_CLAIM`、`PPTS_OIDC_USER_CLAIM`
覆盖 claim 名。配置 OIDC 后默认拒绝 G1 开发身份头；仅本地开发可显式设置 `PPTS_AUTH_DEV_HEADERS=true`
继续允许 `X-PPTS-Tenant-ID` / `X-PPTS-User-ID` fallback。

Web OIDC PKCE 登录使用 Vite 环境变量：`VITE_OIDC_AUTHORIZATION_ENDPOINT`、`VITE_OIDC_TOKEN_ENDPOINT`、
`VITE_OIDC_CLIENT_ID`，可选 `VITE_OIDC_REDIRECT_URI`、`VITE_OIDC_SCOPE`。登录后前端把 token 作为
`Authorization: Bearer` 调用 API；未配置 OIDC 时仍走本地开发身份头。

**角色与 RLS（G3-1，`migrations/0006_rls.sql`/`0012_migrator_role.sql`）**：迁移账号 `ppts_migrator`
持有表 owner 权限，运行时账号 `ppts_app` 必须 `NOSUPERUSER NOBYPASSRLS` 且非表 owner。租户业务表启用 `FORCE ROW LEVEL SECURITY`，
策略依据事务局部 `app.tenant_id`；业务代码通过 `internal/tenant.Run` 设置上下文，缺失上下文时访问被拒绝。
因此 `PPTS_DATABASE_URL` 应使用非 owner 的运行账号（测试库同理）。

跨租户调度（ADR-018，`migrations/0013_scheduler_role.sql`）：`ppts_scheduler` 仅可执行受限函数
`ppts_claim_next_job`，无业务表通用读取权；worker 未设 `PPTS_TENANT_ID` 时按跨租户模式全局领取（按租户在途数初版公平排序），
并可用 `PPTS_SCHEDULER_DATABASE_URL` 提供独立调度连接，handler 执行与终态提交仍走业务连接。
调度并发上限（`migrations/0016_scheduler_concurrency_cap.sql`）：`ppts_claim_next_job` 对 narration 任务
执行 `tenants.policy.max_concurrent_jobs` 并发上限，避免单租户占满队列。

```bash
PPTS_DATABASE_URL='postgres://ppts_app:...@host:5432/ppts' \
PPTS_OBJECT_ROOT='./var/ppts-objects' \
PPTS_OBJECT_SECRET='dev-download-secret' \
GOWORK=off go run ./cmd/api

PPTS_DATABASE_URL='postgres://ppts_app:...@host:5432/ppts' \
PPTS_TENANT_ID='00000000-0000-0000-0000-000000000000' \
PPTS_OBJECT_ROOT='./var/ppts-objects' \
PPTS_TTS_PROVIDER='fake' \
GOWORK=off go run ./cmd/worker
```

`fake` TTS 只生成开发测试用静音 WAV，不构成正式供应商验收。

TTS 供应商（G1-5）由 `PPTS_TTS_PROVIDER` 选择：
- `fake`（默认，开发/测试）：静音 WAV + 估算时长。
- `siliconflow`（正式）：`PPTS_TTS_BASE_URL`（缺省 `https://api.siliconflow.cn`）、`PPTS_TTS_API_KEY`（必填，走环境变量/密钥管理，不写入仓库）、`PPTS_TTS_MODEL`（缺省 `FunAudioLLM/CosyVoice2-0.5B`）、`PPTS_TTS_VOICE`（voice 约定 `<model>:<voice>`，如 `FunAudioLLM/CosyVoice2-0.5B:alex`）。适配器调用 OpenAI 兼容 `/v1/audio/speech`，解码真实时长、规范化供应商占位大 data 长度的 PCM16 WAV（保证 media 链路可解码），无时间戳时构造估算对齐（AlignEstimate）；429/5xx 映射为可重试。

LLM 供应商（G2-2/G2-3）由 `PPTS_LLM_PROVIDER` 选择。`siliconflow` 使用 OpenAI 兼容 `/v1/chat/completions`：`PPTS_LLM_BASE_URL`（缺省 `https://api.siliconflow.cn`）、`PPTS_LLM_API_KEY`（必填，走环境变量/密钥管理，不写入仓库）、`PPTS_LLM_MODEL`（缺省 `Qwen/Qwen2.5-7B-Instruct`）、`PPTS_LLM_VISION_MODEL`（缺省 `Qwen/Qwen3-VL-8B-Instruct`）。当前用于 `ScriptService.GenerateDraft` 的 `SCRIPT_MODE_POLISH` 与 `SCRIPT_MODE_AI_GENERATED`：LLM 生成后由确定性实体校验器检查数字/单位/日期/型号是否保持；若漂移，最多定向修正一次，仍失败则回退原文，保证不产生未经批准的数字改写。

模型网关控制面（G3-11，`migrations/0023_model_gateways.sql`）：TTS/LLM endpoint、API key、模型、视觉模型、音色可通过 Web 顶栏"模型网关"配置，API 端点为 `/api/model-gateways`（admin only，变更写审计）。API key 以 AES-GCM 加密保存到 `model_gateways.encrypted_creds`，密钥来自 `PPTS_GATEWAY_AES_KEY_BASE64`，未设置时回退 `PPTS_BYOS_AES_KEY_BASE64`；读接口只返回 `hasKey/keyMasked`。Worker 运行时按"租户默认 → 平台默认 → env fallback"解析配置并缓存 30s；API 写入后同进程立即失效，跨进程最多 30s 收敛。首次启动且平台行为空时会把 `PPTS_TTS_*` / `PPTS_LLM_*` 幂等种子为平台默认 `env` 网关，保持零配置开发体验。Web 配音生成优先使用默认 TTS 网关的 `voice`，未配置时才回退开发音色。

发音词典（G2-4）：租户级 JSONB 规则表 `pronunciation_dictionaries`，每条规则 `{pattern, replacement, enabled}` 按字面量全局替换 spoken_text，解决 CUDA/MySQL/Kubernetes 等专有名词误读。Worker 合成前加载租户最新词典并应用替换；规则变更自动使合成缓存失效。HTTP CRUD 端点：`GET/POST /api/pronunciation`、`PUT/DELETE /api/pronunciation/{id}`（需认证，tenant 隔离）。

分段级重生成 + 时长控制（G2-5）：`NarrationSnapshot.SegmentIDs` 非空时仅统计目标分段进度，全部分段仍走 `synthesizeSegment`（未改分段命中缓存，零 TTS 开销）；`TargetDurationMS` 非零时首轮合成后计算速率偏差，超出 ±10% 自动按比例调整 `SpeechControl.RatePercent`（50–200% 范围）重合成。

LLM token 预算（G2-5）：`usage.KindLLMTokens` 计量文案生成 token。每次 LLM 调用前按 `EstimateLLMTokens`（中文 1.5 字符/token 粗估）预占，完成后按供应商真实 prompt+completion 结算；超出额度任务失败。

内容哈希去重（G2-7）：分段音频按内容寻址（synthesis configHash）存放在租户级共享路径 `{tenant}/shared/cache/audio/{configHash}.{ext}`，分段清单存 `shared/cache/segments/{configHash}.json`。相同合成配置（文本+音色+语率+模型）跨页面/跨项目直接复用，零 TTS 调用、零重复存储；命中率经 `ppts_tts_cache_hit_total`（scope=project/shared）观测。

评测集与审核门禁（G2-6）：`scripts/gen_corpus` 生成 13 套共 100 页语料（含数字/单位/型号实体，总页数 <100 时显式失败）；`scripts/ai_eval` 运行评测门禁——逐页 LLM polish 后用确定性实体校验器检查关键数字是否忠于来源，报告写 `testdata/ai-eval/report.json`（passRate/P95），任一页实体漂移即失败；无 LLM 供应商时优雅跳过。人工审核门禁：配音须 `Approve`/`Lock`（`RequireConfirmed`，API 与 worker 双侧校验），AI 草稿不覆盖既有稿件。

来源锚点（G2-1）：`ScriptRevision.segments[].source_anchors` 返回结构化 provenance（`slide_id`/`shape_id`/`kind`/`raw`/`confidence`）。解析文档中的 shape 文本生成草稿时写入 `shape_*` anchors（结构通道 confidence=1.0），备注 fallback 写入 `notes` anchor；若解析阶段已产生页面 PNG 且配置视觉模型，则再从 `render/page-NNNN.png` 提取 `visual_*` anchors（低置信、待审核证据）。视觉通道是可选增强，页面图或模型不可用时不影响草稿生成。用户编辑讲稿时服务端按 `segment_id` 保留既有 anchors，不接受客户端伪造来源。旧版 `source_refs` 继续保留。

渲染（G0-2）：`internal/integrations/render` 使用 LibreOffice→PDF→poppler→PNG。二进制路径可经 `PPTS_SOFFICE_BIN`/`PPTS_PDFTOPPPM_BIN`/`PPTS_PDFINFO_BIN` 覆盖，渲染临时目录根用 `PPTS_RENDER_WORK_ROOT`（当 soffice 为 Windows `.exe` 且经 WSL interop 运行时必须指向 `/mnt/<drive>` 下，如 `D:\LibreOffice\program\soffice.exe`）。

对象存储后端（G3-6）：`PPTS_OBJECT_BACKEND` 指定默认后端（缺省 `local`）。
`local` 使用 `PPTS_OBJECT_ROOT`（`PPTS_OBJECT_SECRET` 用于本地签名链接）；
`s3` 使用 `PPTS_S3_ENDPOINT`/`PPTS_S3_BUCKET`/`PPTS_S3_ACCESS_KEY`/`PPTS_S3_SECRET_KEY`/`PPTS_S3_REGION`/`PPTS_S3_USE_SSL`。
租户 `tenants.policy.storage_backend` 可覆盖默认后端，未注册后端会显式失败。
租户 `tenants.policy.storage_region` 用于 S3 兼容后端按区域选择桶：对象路由到 `{PPTS_S3_BUCKET}-{region}`（懒创建并缓存），
区域为空时回退基础桶；生命周期规则同样落到对应区域桶。
存储生命周期下发：租户策略 `storage_transition_days`/`storage_expiration_days` 显式配置时，worker 通过
`PPTS_STORAGE_LIFECYCLE_INTERVAL`（默认 6h）周期调用对象存储生命周期接口；`source_retention_days` 仍由
数据库保留清理精确处理，不映射为桶级过期规则。
存储占用可见性：API/worker 对象存储写入会同步维护 `object_inventory`；`TenantService.StorageUsage` 按源上传、
artifact 与清单中未被前两者覆盖的 work/audio/export/archive 等对象汇总字节数/对象数，不依赖对象存储 List。
BYOS 凭据（G3-6，`migrations/0015_byos_credentials.sql`）：`byos_credentials` 按租户 RLS 隔离，配置以 AES-GCM
密文保存（AAD 绑定 tenant/credential/backend），`kms_key_id` 记录外部包裹密钥标识；租户策略
`storage_backend=byos:<credential_id>` 且设置 `PPTS_BYOS_AES_KEY_BASE64` 时，Registry 会按需解密凭据并动态构建
S3 兼容后端。
对象信封加密：设置 `PPTS_OBJECT_ENCRYPTION_KEY_BASE64` 后，租户策略 `envelope_encryption=true` 的对象 Put/Get
会在服务端 AES-GCM 加/解密；为避免绕过服务端加密，启用加密租户的直接预签名读写会返回不支持。

API 暴露 `/debug/vars`（无需业务身份头）用于本地/CI 读取 expvar 指标。worker 已接入基础任务指标：
`ppts_worker_jobs_total`（按事件/终态）、`ppts_worker_job_duration_ms_total`（执行时长）、
`ppts_worker_queue_wait_ms_total`（领取时按 `CreatedAt` 记录的队列等待时长，均值=sum/claimed）、
`ppts_worker_queue_oldest_wait_seconds`（队列积压最老任务等待时长，由 `migrations/0017_queue_backlog.sql`
受限函数 + worker 周期报告器更新，`PPTS_QUEUE_BACKLOG_INTERVAL` 默认 30s），
以及 TTS 指标 `ppts_tts_synthesis_total`、`ppts_tts_synthesis_duration_ms_total`、`ppts_tts_throttled_total`。
指标键默认只按 kind×event 聚合，避免以租户 UUID 作为高基数标签；排障时设置 `PPTS_METRICS_TENANT_LABELS=true`
可启用租户维度。
worker 日志统一为结构化 JSON（slog），后台循环（保留清理/审计归档/生命周期/队列积压）经
`slog.NewLogLogger` 适配器输出，与 worker 自身日志一致。
链路追踪（G3-8）：API RPC 通过 Connect interceptor 生成 span；创建任务时把 W3C `traceparent` 持久化到
`jobs.traceparent`（`migrations/0018_job_traceparent.sql`），worker 执行时以 `SpanLink` 关联 API span，实现
跨进程异步任务链路。未设置 `PPTS_OTEL_EXPORTER_OTLP_ENDPOINT` 时保持 no-op，零依赖运行；设置后经 OTLP 导出。

租户策略 `tenants.policy.max_concurrent_jobs`（>0 时生效）限制配音生成的非终态任务数，超限返回 `ResourceExhausted`；
同 `Idempotency-Key` 重放不受上限影响，仍返回既有任务。

审计日志（G3-4，`migrations/0009_audit.sql`）：`audit_events` 按租户隔离（FORCE RLS），记录任务取消/重试与
保留清理删除等操作；`TenantService.ListAuditEvents` 提供 admin+ 审计读取 API，可按 action/resource_type/since 过滤。
到期审计事件由 worker 归档到对象存储（JSONL）后清除：`PPTS_AUDIT_RETENTION_DAYS`（默认 365）控制保留期，
`PPTS_AUDIT_ARCHIVE_INTERVAL`（默认 1h）控制执行周期；`TenantService.ListAuditArchives` 按 `object_inventory`
返回归档对象清单（admin+）。

租户生命周期（G3-4，`migrations/0011_tenant_status.sql`）：`tenants.status` 为 `active/suspended/deleted`；
API 默认检查租户 active 状态，停用/删除租户请求在进入 RPC 前返回 403/PermissionDenied。

用量与成本：`TenantService.Usage` 汇总当月生成秒数及成本分账金额，`TenantService.ProjectUsage` 按项目返回
累计生成秒数、配音任务数与用户/供应商两类金额（账本经幂等键归属 narration 任务，G3-8）。
定价表在 `internal/pricing`（每计量种类用户价/供应商成本/币种/版本），`PPTS_PRICE_BOOK` JSON 覆盖，
缺省为占位价目；`migrations/0020_price_costs.sql` 在结算时同事务写入 `user_amount`/`supplier_cost`/`currency`，
实现"供应商成本 vs 用户计费"分账（G3-2）。
数据导出/擦除：`TenantService.ExportTenant/PurgeTenant`（owner+）把租户全部业务数据按表 JSONL 导出到对象存储，
或执行对象 GC + 逐表清除并置 `tenants.status='deleted'`（`internal/tenant.ExportTenant/PurgeTenant`，PG 门禁覆盖）。

租户成员/角色（G3-3 内核，`migrations/0010_members.sql`）：`tenant_members` 持久化
Owner/Admin/Editor/Reviewer/Viewer，`TenantService.Members/Roles` 可读，`SetMemberRole/RemoveMember` 可管理成员
（admin+，owner 变更仅 owner）；配音生成、任务取消/重试、Project/Script/Upload/Export 核心写入口均有角色门禁
（未配置成员读取时开发放行）；后续分享/声音/费用权限矩阵待对应 RPC 落地时逐项标注与测试。

API 结构化请求日志（G3-8）：每个请求带 `X-Request-ID`，JSON slog 输出
`request_id/method/path/status/duration_ms/bytes/tenant/user`。

任务事件（G3-9）：`WatchEvents` 基于 `migrations/0019_job_events.sql` 的 `job_events` 专用事件表，
任务状态/进度变更在事务内写入单调 seq 的事件快照，客户端可用 `after_seq` 断点续传，避免复用 `updated_at`
同毫秒并发更新漏发。进度上报：worker 注入 `pipeline.ReportProgress(ctx, pct)`，handler 调用即可更新
任务进度（fencing 条件），NarrationHandler 按已合成 segment 数上报 0-100%。
outbox（G3-5）：handler 可调用 `pipeline.SetCommitStep(ctx, step)` 声明"完成后才存在的最终产物步骤"，
worker 在 `CompleteWithStep` 中与任务终态同一事务原子 upsert（fencing 条件，失败整体回滚），
避免"步骤成功但任务未终态"的崩溃窗口；`ExportHandler` 已接入。

worker 还运行数据保留清理循环（G3-7）：可配 `PPTS_RETENTION_INTERVAL`（默认 `1h`）与
`PPTS_UPLOAD_ABANDON_TTL`（默认 `24h`）。清理项包括超过项目 `source_retention_days` 的源对象
（项目未设时回退租户策略 `source_retention_days` 默认）、选择"处理后删除"且解析成功的源对象，
以及超时仍 `pending` 的上传临时对象。派生产物按租户策略分档过期：`artifact_retention_days`（导出成品）、
`audio_retention_days`（配音音频）、`render_retention_days`（页面渲染图），过期后删除对象与清单记录
（artifact 同时删除 `artifacts` 行）并写 `derived.delete` 审计。
同一循环还清理超过 `PPTS_QUOTA_RESERVATION_TTL`（默认 `24h`）仍处于 `reserved` 的额度预占。
