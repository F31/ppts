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
- `ScriptService`：`Get/Update/Approve/Lock/GenerateDraft`（原文讲稿生成）；`NarrationService.CreateGeneration`；`ExportService`；`PlaybackService.GetNarration/GetManifest`（GetNarration 发现最近成功配音时间轴，容器化页面渲染未就绪时 GetManifest 允许无页面图）。

Web 真实链路：直传 → 解析 → 展示真实页面 rail → 生成原文讲稿 → 生成配音 → 拉取真实播放 manifest（音频+字幕）→ 导出下载；`local://` 预签名链接（上传、播放、导出）统一由 API 的 `/ppts/object/{key}` 端点重写服务，S3 后端返回原生预签名 URL。真实链路的 HTTP 端到端验收测试见 `internal/api/e2e_test.go`（`-tags=pg`，真实 PG + 本地存储 + 真实 worker，TTS 用 fake）。

## 本地服务

API 依赖已应用迁移的 PostgreSQL。G1 开发身份由可信上游头
`X-PPTS-Tenant-ID` / `X-PPTS-User-ID` 注入；G3 将替换为 OIDC 校验。

**角色与 RLS（G3-1，`migrations/0006_rls.sql`/`0012_migrator_role.sql`）**：迁移账号 `ppts_migrator`
持有表 owner 权限，运行时账号 `ppts_app` 必须 `NOSUPERUSER NOBYPASSRLS` 且非表 owner。租户业务表启用 `FORCE ROW LEVEL SECURITY`，
策略依据事务局部 `app.tenant_id`；业务代码通过 `internal/tenant.Run` 设置上下文，缺失上下文时访问被拒绝。
因此 `PPTS_DATABASE_URL` 应使用非 owner 的运行账号（测试库同理）。

跨租户调度（ADR-018，`migrations/0013_scheduler_role.sql`）：`ppts_scheduler` 仅可执行受限函数
`ppts_claim_next_job`，无业务表通用读取权；worker 未设 `PPTS_TENANT_ID` 时按跨租户模式全局领取（按租户在途数初版公平排序），
并可用 `PPTS_SCHEDULER_DATABASE_URL` 提供独立调度连接，handler 执行与终态提交仍走业务连接。

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

`fake` TTS 只生成开发测试用静音 WAV，不构成 G1-5 正式供应商验收。

对象存储后端（G3-6）：`PPTS_OBJECT_BACKEND` 指定默认后端（缺省 `local`）。
`local` 使用 `PPTS_OBJECT_ROOT`（`PPTS_OBJECT_SECRET` 用于本地签名链接）；
`s3` 使用 `PPTS_S3_ENDPOINT`/`PPTS_S3_BUCKET`/`PPTS_S3_ACCESS_KEY`/`PPTS_S3_SECRET_KEY`/`PPTS_S3_REGION`/`PPTS_S3_USE_SSL`。
租户 `tenants.policy.storage_backend` 可覆盖默认后端，未注册后端会显式失败。

API 暴露 `/debug/vars`（无需业务身份头）用于本地/CI 读取 expvar 指标。worker 已接入基础任务指标：
`ppts_worker_jobs_total`（按事件/终态）、`ppts_worker_job_duration_ms_total`（执行时长）、
`ppts_worker_queue_wait_ms_total`（领取时按 `CreatedAt` 记录的队列等待时长，均值=sum/claimed），
以及 TTS 指标 `ppts_tts_synthesis_total`、`ppts_tts_synthesis_duration_ms_total`、`ppts_tts_throttled_total`，
均按租户与任务类型聚合。

租户策略 `tenants.policy.max_concurrent_jobs`（>0 时生效）限制配音生成的非终态任务数，超限返回 `ResourceExhausted`；
同 `Idempotency-Key` 重放不受上限影响，仍返回既有任务。

审计日志（G3-4，`migrations/0009_audit.sql`）：`audit_events` 按租户隔离（FORCE RLS），记录任务取消/重试与
保留清理删除等操作；`TenantService.ListAuditEvents` 提供 admin+ 审计读取 API，可按 action/resource_type/since 过滤。
到期审计事件由 worker 归档到对象存储（JSONL）后清除：`PPTS_AUDIT_RETENTION_DAYS`（默认 365）控制保留期，
`PPTS_AUDIT_ARCHIVE_INTERVAL`（默认 1h）控制执行周期。

租户生命周期（G3-4，`migrations/0011_tenant_status.sql`）：`tenants.status` 为 `active/suspended/deleted`；
API 默认检查租户 active 状态，停用/删除租户请求在进入 RPC 前返回 403/PermissionDenied。

用量可见性：`TenantService.Usage` 汇总当月生成秒数，`TenantService.ProjectUsage` 按项目返回累计生成秒数与
配音任务数（账本经幂等键归属 narration 任务，G3-8；成本金额随正式定价表接入）。
数据导出/擦除：`TenantService.ExportTenant/PurgeTenant`（owner+）把租户全部业务数据按表 JSONL 导出到对象存储，
或执行对象 GC + 逐表清除并置 `tenants.status='deleted'`（`internal/tenant.ExportTenant/PurgeTenant`，PG 门禁覆盖）。

租户成员/角色（G3-3 内核，`migrations/0010_members.sql`）：`tenant_members` 持久化
Owner/Admin/Editor/Reviewer/Viewer，`TenantService.Members/Roles` 可读，`SetMemberRole/RemoveMember` 可管理成员
（admin+，owner 变更仅 owner）；配音生成、任务取消/重试、Project/Script/Upload/Export 核心写入口均有角色门禁
（未配置成员读取时开发放行）；OIDC 身份与后续分享/声音/费用权限矩阵待后续。

API 结构化请求日志（G3-8）：每个请求带 `X-Request-ID`，JSON slog 输出
`request_id/method/path/status/duration_ms/bytes/tenant/user`。

worker 还运行数据保留清理循环（G3-7）：可配 `PPTS_RETENTION_INTERVAL`（默认 `1h`）与
`PPTS_UPLOAD_ABANDON_TTL`（默认 `24h`）。清理项包括超过项目 `source_retention_days` 的源对象、
选择"处理后删除"且解析成功的源对象，以及超时仍 `pending` 的上传临时对象。
同一循环还清理超过 `PPTS_QUOTA_RESERVATION_TTL`（默认 `24h`）仍处于 `reserved` 的额度预占。
