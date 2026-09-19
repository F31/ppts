# PPTS 功能与 API 说明书

> 版本：7d2f89f-dirty（开发版）  
> 协议层：Connect RPC（gRPC-Web） + 原生 HTTP/JSON  
> 端口：`:8080`（单机部署）/ `:80`（nginx 反代）

---

## 一、系统概述

PPTS（**智讲 PPT**）是一个基于 Go 的单机/SaaS 服务，将 PPT 幻灯片转换为带真人语音讲解的视频或 Web 播放工程。

**核心链路：**  
上传 PPTX → 解析为幻灯片 → AI 生成/润色讲稿 → 选音色配音 → 合成 MP4 或 Web 讲解 → 公开分享

**部署形态：**
- **二进制发行版**（Kimi 方式）：单 `ppts` 二进制，内嵌前端静态资源与数据库迁移 SQL；`server` + `worker` 双子命令，由 systemd 托管。
- **Docker Compose**：`ppts-api`（API + 前端）+ `ppts-worker`（异步任务），两者共用同一 PostgreSQL。

---

## 二、技术栈

| 层次 | 技术选型 |
|------|---------|
| 语言 | Go 1.25（CGO_ENABLED=0，纯静态链接） |
| 服务框架 | Connect RPC（buf.build/gen/go/connectrpc/go/protocolbuffers-go） |
| 数据持久化 | PostgreSQL 16（pgx/v5），RLS 租户隔离 |
| 前端 | React 18 + TypeScript + Vite，SPA（go:embed 内嵌） |
| 认证 | HS256 JWT（邮箱登录）、OIDC（生产）、Dev Headers（联调） |
| 对象存储 | `local`（单机）/ `s3`（S3 兼容，如 MinIO、阿里云 OSS） |
| 异步任务 | Worker 进程消费 PostgreSQL 任务队列（pgbouncer-free） |
| 媒体处理 | 静态 ffmpeg（johnvansickle，带 libass）+ LibreOffice headless + poppler-utils |

---

## 三、数据库模型（22 张表）

### 3.1 租户与用户体系

```
tenants ──┬── projects          # 项目（每个项目属于一个租户）
          ├── tenant_members    # 成员关系
          ├── tenant_quotas     # 配额（月度用量上限）
          └── users             # 用户账号（跨租户）
                    │
                    └── credentials   # 邮箱+密码哈希（bcrypt + pepper）
```

### 3.2 项目核心

```
projects
  ├── source_revisions          # PPTX 源文件版本（每次上传生成一条）
  │       └── uploads           # 大文件分片上传记录
  ├── narration_scripts         # 讲稿（按幻灯片分页存储，含 revision_id）
  │       └── narration_segments # 讲稿段落级版本锚点（支持局部重生成）
  └── slide_script_sources      # 每页讲稿来源（原文/润色/AI生成 标记）
```

### 3.3 任务与成品

```
jobs                            # 异步任务（parse/render/script/narration/export）
  ├── job_steps                 # 任务执行步骤（每步状态/耗时/traceId）
  └── job_events                # 任务事件流（SSE WatchEvents 读取）

artifacts                       # 成品（不可变快照）
  └── object_inventory          # 对象存储引用（去重，按 content_hash）
```

### 3.4 公开区与审计

```
publications                    # 公开作品（含 public_id / 审核状态 / 精选标记）

model_gateways                  # 模型网关配置（TTS/LLM，凭据 AES-GCM 加密存储）

pronunciation_dictionaries      # 发音词典（自定义读音纠正）

audit_events                    # 审计日志（操作记录，保留归档）

usage_ledger                    # 用量账本（按月聚合，供 Quota 检查）
quota_reservations              # 配额预占（任务提交时预扣，完成后结算）

byos_credentials                # BYOS（自带存储）凭据
```

---

## 四、Connect RPC 接口（proto 定义）

所有 RPC 路径前缀：`/ppts.v1.{Service}/{Method}`  
认证方式：`Authorization: Bearer <jwt>` 或 Dev Headers（`X-PPTS-Tenant-ID` + `X-PPTS-User-ID`）

---

### 4.1 ProjectService — 项目管理

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `Create` | `CreateProjectRequest{title}` | `CreateProjectResponse{project}` | 新建空项目 |
| `Get` | `GetProjectRequest{id}` | `Project` | 获取项目详情 |
| `List` | `ListProjectsRequest{cursor, page_size}` | `ListProjectsResponse{projects, next_cursor}` | 分页列表 |
| `GetSlides` | `GetSlidesRequest{project_id, revision_no}` | `GetSlidesResponse{revision_no, slides[]}` | 获取幻灯片列表（slide_id/index/title/preview/has_notes/feature_flags） |
| `CreateSourceRevision` | `CreateSourceRevisionRequest{project_id, object_key, source_hash, size_bytes, parser_version}` | `SourceRevision` | 上传新 PPTX 版本 |
| `Archive` | `ArchiveProjectRequest{id}` | `Project` | 归档项目（软删除） |

**Project 消息字段：**  
`id, tenant_id, owner, title, current_revision, policy, archived, created_at_unix`

**SlideSummary 字段：**  
`slide_id, index, title, preview, has_notes, feature_flags[]`

---

### 4.2 ScriptService — 讲稿管理

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `Get` | `GetScriptRequest{project_id, slide_id}` | `ScriptRevision` | 获取单页讲稿 |
| `Update` | `UpdateScriptRequest{project_id, slide_id, content, mode, expected_revision}` | `UpdateScriptResponse{script, latest_revision}` | 保存讲稿（乐观锁，冲突时返回 latest_revision） |
| `GenerateDraft` | `GenerateDraftRequest{project_id, slide_ids[], mode, context}` | `GenerateDraftResponse{jobs[]}` | AI 生成讲稿草稿（ORIGINAL/POLISH/AI_GENERATED） |
| `Approve` | `ApproveScriptRequest{project_id, slide_id, revision_id}` | `ScriptRevision` | 确认讲稿（锁定，标记为最终版本） |
| `Lock` | `LockScriptRequest{project_id, slide_id}` | `ScriptRevision` | 锁定讲稿（防止编辑） |

**ScriptMode 枚举：** `SCRIPT_MODE_ORIGINAL(1) / SCRIPT_MODE_POLISH(2) / SCRIPT_MODE_AI_GENERATED(3)`

---

### 4.3 NarrationService — 配音生成

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `Estimate` | `NarrationEstimateRequest{project_id, slide_ids[], voice_id, mode}` | `NarrationEstimateResponse{duration_seconds, cost_estimate}` | 配音费用预估 |
| `CreateGeneration` | `CreateGenerationRequest{project_id, slide_ids[], voice_id, tone_id}` | `CreateGenerationResponse{job_id}` | 启动全篇配音生成任务 |
| `RegenerateSegments` | `RegenerateSegmentsRequest{project_id, slide_ids[], voice_id}` | `RegenerateSegmentsResponse{jobs[]}` | 局部重生成（仅变更段落） |

---

### 4.4 PlaybackService — 播放与合成

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `GetNarration` | `GetNarrationRequest{project_id}` | `GetNarrationResponse{narration_status}` | 轮询配音进度（queued/running/done/error） |
| `GetManifest` | `GetPlaybackManifestRequest{project_id}` | `PlaybackManifest{pages[], timeline}` | 获取播放清单（页序+时间轴+音频 URL） |

---

### 4.5 JobService — 任务管理

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `Get` | `GetJobRequest{job_id}` | `Job` | 单任务详情 |
| `List` | `ListJobsRequest{project_id, state, cursor, page_size}` | `ListJobsResponse{jobs[], next_cursor}` | 分页列表 |
| `Cancel` | `CancelJobRequest{job_id}` | `Job` | 取消任务 |
| `RetryFailed` | `RetryFailedRequest{job_id}` | `Job` | 重试失败任务 |
| `WatchEvents` | `WatchEventsRequest{project_id, after_seq}` | `stream JobEvent` | SSE 实时事件流 |

**JobState 枚举：**  
`QUEUED(1) / RUNNING(2) / RETRY_WAIT(3) / WAITING_REVIEW(4) / SUCCEEDED(5) / FAILED(6) / CANCEL_REQUESTED(7) / CANCELED(8) / UNKNOWN_PROVIDER_RESULT(9)`

**Job 字段：**  
`job_id, project_id, kind(parse/render/script_draft/narration/export), state, attempt, progress_percent, last_error, input_snapshot, created_at_unix, updated_at_unix`

---

### 4.6 ExportService — 成品导出

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `CreateExport` | `CreateExportRequest{project_id, format, slide_ids[], burn_subtitles, include_notes, timeline_key, page_png_keys}` | `CreateExportResponse{job_id}` | 创建导出任务 |
| `GetArtifact` | `GetArtifactRequest{artifact_id}` | `Artifact` | 查询成品元数据 |
| `CreateDownload` | `CreateDownloadRequest{artifact_id, ttl_seconds}` | `CreateDownloadResponse{signed_url, expires_at_unix}` | 生成短期预签名下载链接 |

**ArtifactFormat 枚举：**  
`WEB_PROJECT(1) / MP4(2) / AUDIO_PACK(3) / SUBTITLE_SRT(4) / SUBTITLE_VTT(5) / AUDIO_PPTX(6)`

**Artifact 字段：**  
`artifact_id, project_id, snapshot_id, format, object_key, content_hash, size_bytes, created_at_unix, snapshot_hash`

---

### 4.7 UploadService — 大文件上传

| RPC | 入参 | 出参 | 说明 |
|-----|------|------|------|
| `CreateUpload` | `CreateUploadRequest{project_id, filename, mime_type, size_bytes}` | `CreateUploadResponse{upload_id, parts[]}` | 初始化分片上传，返回各分片上传地址 |
| `CompleteUpload` | `CompleteUploadRequest{upload_id, partETags[]}` | `CompleteUploadResponse{object_key}` | 合并分片，返回目标 object_key |
| `AbortUpload` | `AbortUploadRequest{upload_id}` | `AbortUploadResponse{}` | 取消分片上传 |

---

### 4.8 TenantService — 租户管理（admin+）

| RPC | 权限 | 说明 |
|-----|------|------|
| `Members` | admin | 成员列表 |
| `Roles` | admin | 角色列表 |
| `SetMemberRole` | admin | 修改成员角色 |
| `RemoveMember` | admin | 移除成员 |
| `ExportTenant` | admin | 导出租户所有数据（打包下载） |
| `PurgeTenant` | admin | 彻底清除租户数据 |
| `Quota` | owner | 月度配额查询 |
| `Usage` | owner | 月度用量明细 |
| `ProjectUsage` | owner | 各项目的用量分布 |
| `StorageUsage` | owner | 存储空间使用量 |
| `Policy` | owner | 租户策略（存储后端/保留期） |
| `ListAuditEvents` | admin | 审计事件列表（支持 action/resource_type/since 过滤） |
| `ListAuditArchives` | admin | 审计归档列表 |

---

## 五、原生 HTTP/JSON 接口

路径不含 `/ppts.v1.*` 前缀，部分接口无需认证（公开区），其余均需 token 或 dev headers。

### 5.1 健康检查（匿名）

```
GET /healthz                          → "ok"（纯文本）
```

### 5.2 邮箱认证（匿名）

```
GET  /auth/config                     → {email_password: bool, local?: bool, tenant_type?}
                                      （探测邮箱注册能力；local=true 为 SQLite 单租户本地模式）
POST /auth/register                   → Body: {email, password(≥8位), account_type: personal|organization, org_name?(组织必填)}
                                      → {access_token, account, tenant_id, tenant_name, tenant_type, user_id}
POST /auth/email-login                → Body: {email, password}
                                      → {access_token, account, tenant_id, tenant_name, tenant_type, user_id}
```

**JWT 说明：** HS256 签名，payload 含 `sub(user_id), tti(tenant_id), iss, aud, exp, iat`。  
**密码存储：** bcrypt + 全局 pepper（`PPTS_PASSWORD_PEPPER` 环境变量，不落库）。

### 5.3 对象存储（受保护）

```
GET  /ppts/object/{key...}           → 读取对象（预签名 URL 重写，local/S3 均适用）
PUT  /ppts/object/{key...}           → 写入对象
DELETE /ppts/object/{key...}         → 删除对象
```

### 5.4 项目辅助路由

```
GET  /projects/{pid}/artifacts       → 按项目列出品（JSON 数组）
GET  /projects/{pid}/slides/render   → 渲染页缩略图 URL 列表（JSON key=slide_id）
PUT  /projects/{pid}/slides/{sid}/source → 持久化讲稿来源（JSON {kind, source_id}）
GET  /projects/{pid}/slides/sources  → 读取讲稿来源映射
GET  /projects/{pid}/narration/draft-count → 待确认讲稿数
GET  /projects/{pid}/narration/stale     → 已过时的讲稿页 ID 列表
```

### 5.5 任务辅助路由

```
GET  /jobs/page?projectId=&phase=&sort=&dir=&limit=&cursor=  → 分页任务列表 + 阶段计数
GET  /jobs/summary?ids=a,b,c                                     → 批量范围摘要
GET  /jobs/{jid}/detail                                          → 任务详情（步骤/traceId/范围）
```

**/jobs/page 响应结构：**
```json
{
  "jobs": [{
    "jobId": "...", "projectId": "...", "kind": "parse|render|script_draft|narration|export",
    "state": "JOB_STATE_SUCCEEDED|FAILED|...", "attempt": 1,
    "progressPercent": 100, "phase": "",
    "createdAtUnix": 1789426136, "updatedAtUnix": 1789426136
  }],
  "nextCursor": "",
  "phaseCounts": {"parse": 3, "narration": 1}
}
```

### 5.6 模型网关管理（admin+）

```
GET    /api/model-gateways?[kind=tts|llm]       → 列表
POST   /api/model-gateways                      → 创建
PUT    /api/model-gateways/{name}               → 更新
DELETE /api/model-gateways/{name}               → 删除
POST   /api/model-gateways/{name}/set-default   → 设为默认
POST   /api/model-gateways/{name}/test          → 连通性测试
```

**凭据存储：** AES-GCM 加密，密钥来自 `PPTS_GATEWAY_AES_KEY_BASE64`（32 字节 base64）。

### 5.7 发音词典（受保护）

```
GET    /api/pronunciation                     → 列表
POST   /api/pronunciation                     → 创建
PUT    /api/pronunciation/{id}                → 更新
DELETE /api/pronunciation/{id}                → 删除
```

### 5.8 公开区（匿名读，受保护写）

```
GET  /public/works?[kind=featured|user]&limit=N        → 作品广场（匿名）
GET  /public/works/mine                                → 我的发布（需认证）
GET  /public/works/queue                               → 审核队列（admin）
POST /public/works                                     → 发布作品（需认证）
POST /public/works/{publicId}/recall                   → 撤回发布（owner/admin）
POST /public/featured                                  → 设为精选（admin）
PUT  /public/works/{id}/review                         → 审核通过/拒绝（admin）
DELETE /public/works/{id}                              → 删除作品（admin）
GET  /showcase/{publicId}                              → 作品详情（匿名，含封面元数据）
GET  /showcase/{publicId}/manifest                     → 播放清单（匿名，含音频+字幕 URL）
```

### 5.9 成品库（owner 级）

```
GET /artifacts           → 跨项目成品列表（JSON 数组）
```

---

## 六、认证与权限模型

### 6.1 身份类型

| 类型 | 凭证 | 适用场景 |
|------|------|---------|
| **邮箱 JWT** | `Authorization: Bearer <token>` | 生产邮箱注册登录 |
| **OIDC Bearer** | `Authorization: Bearer <oidc_token>` | 企业 SSO |
| **Dev Headers** | `X-PPTS-Tenant-ID` + `X-PPTS-User-ID` | 本地联调（需 `PPTS_AUTH_DEV_HEADERS=true`） |

### 6.2 角色体系（5 级）

| 角色 | 数值 | 能力门禁 |
|------|------|---------|
| viewer | 0 | 只读项目 |
| reviewer | 1 | 审阅讲稿（approve/lock） |
| editor | 2 | 创建项目、编辑讲稿、生成配音、导出、管理任务 |
| admin | 3 | 成员管理、模型网关、审计日志、公开区管理 |
| owner | 4 | 用量查询、配额策略、完整租户管理 |

### 6.3 租户隔离

所有业务表启用 `FORCE ROW LEVEL SECURITY`，通过事务局部 `app.tenant_id` GORM scope 控制行级访问。运行时账号 `ppts_app` 必须是 `NOSUPERUSER NOBYPASSRLS` 且非表 owner。

---

## 七、Worker 任务流程

### 7.1 任务类型

| kind | 触发时机 | 执行内容 |
|------|---------|---------|
| `parse` | 上传 PPTX 后 | LibreOffice 导出 PDF → poppler 逐页 PNG → 提取备注 |
| `render` | 编辑器请求时 | 按需渲染幻灯片缩略图 |
| `script_draft` | AI 生成讲稿时 | LLM 调用（TTS 网关中的 LLM endpoint） |
| `narration` | 配音生成时 | TTS 合成 → ffmpeg 合成 MP4/音频包 |
| `export` | 导出成品时 | 综合合成（页面 PNG + 音频 + 字幕烧录） |

### 7.2 执行约束

- Worker 并发：`PPTS_SCHEDULER_CONCURRENCY_CAP`（默认无限制）
- 任务重试：失败自动重试，最多 3 次（`attempt` 字段追踪）
- 调度账号 `ppts_scheduler` 仅持有 `EXECUTE` 权限，不直接接触业务数据
- SSE 事件写入 `job_events` 表，保留窗口由 `PPTS_JOB_EVENTS_RETENTION` 控制

---

## 八、对象存储后端

### 8.1 local 后端（单机默认）

- 根目录：`PPTS_OBJECT_ROOT`（默认 `/var/lib/ppts/objects`）
- Key 格式：`{tenant_id}/{project_id}/...`
- 读取：`GET /ppts/object/{key}` 重写为本地文件读取
- 签名：`PPTS_OBJECT_SECRET`（32 字节 base64，AES-GCM）

### 8.2 s3 后端（生产）

- 桶名：`{PPTS_S3_BUCKET}-{region}`（懒创建）
- 预签名 URL：直接返回原生 S3 PresignedURL（不经过 `/ppts/object/`）
- 生命周期：由租户策略 `storage_transition_days` / `storage_expiration_days` 驱动

---

## 九、配置变量

### 9.1 必配项

| 变量 | 示例 | 说明 |
|------|------|------|
| `PPTS_DATABASE_URL` | `postgres://ppts_app:pw@host:5432/db?sslmode=disable` | 应用连接串（ppts_app 账号） |
| `PPTS_SCHEDULER_DATABASE_URL` | `postgres://ppts_scheduler:pw@host:5432/db?sslmode=disable` | 调度账号连接串 |
| `PPTS_HTTP_ADDR` | `:8080` | API 监听地址 |
| `PPTS_OBJECT_BACKEND` | `local` / `s3` | 存储后端 |
| `PPTS_OBJECT_ROOT` | `/var/lib/ppts/objects` | local 模式数据根目录 |
| `PPTS_OBJECT_SECRET` | `base64(32bytes)` | 本地存储签名密钥 |
| `PPTS_JWT_SECRET` | `base64(32bytes)` | JWT HS256 签名密钥 |
| `PPTS_PASSWORD_PEPPER` | `base64(32bytes)` | 密码哈希 pepper |

### 9.2 可选项

| 变量 | 说明 |
|------|------|
| `PPTS_AUTH_DEV_HEADERS` | `true` 时允许 X-PPTS-Tenant-ID/X-PPTS-User-ID 开发头 |
| `PPTS_OIDC_ISSUER` / `PPTS_OIDC_CLIENT_ID` | OIDC 生产登录（配置后邮箱登录仍可用） |
| `PPTS_TTS_PROVIDER` | `fake`（开发）/ `elevenlabs` / `azure` / 自定义 |
| `PPTS_TTS_BASE_URL` / `API_KEY` / `MODEL` / `VOICE` | TTS 接入参数 |
| `PPTS_GATEWAY_AES_KEY_BASE64` | 模型网关凭据 AES 加密密钥 |
| `PPTS_WEB_ROOT` | 前端静态目录（空时自动使用内嵌 dist） |
| `TZ` | 时区，默认 `Asia/Shanghai` |

---

## 十、前端页面一览

| 页面路由 | 功能 | 权限 |
|---------|------|------|
| `/login` | 三种登录入口（Dev/OIDC/邮箱） | 匿名 |
| `/home` | 首页：项目卡片、待办事项、用量摘要 | 所有已登录用户 |
| `/projects` | 项目列表：创建/归档/导入 | editor+ |
| `/projects/:id/editor` | 编辑器：三栏（幻灯片轨/预览/讲稿）+ 配音+导出 | editor+ |
| `/projects/:id/artifacts` | 项目产物页：成品列表与下载 | editor+ |
| `/jobs` | 任务中心：实时 SSE + 详情/取消/重试 | editor+ |
| `/watch/:publicId` | 公开作品播放（匿名） | 匿名 |
| `/library` | 跨项目成品库 | owner |
| `/explore` | 公开广场（匿名浏览） | 匿名 |
| `/public-admin` | 公开区管理：我的发布+审核队列 | admin |
| `/settings/members` | 成员管理 | admin |
| `/settings/models` | 模型网关管理 | admin |
| `/settings/usage` | 用量与存储 | owner |
| `/settings/audit` | 审计日志 | admin |
| `/settings/dictionary` | 发音词典 | editor+ |

---

## 十一、未启用/规划中功能

| 功能 | 状态 | 阻塞条件 |
|------|------|---------|
| OIDC 生产登录 | 代码就绪，未配置 | 需设置 `PPTS_OIDC_ISSUER` + `PPTS_OIDC_CLIENT_ID` |
| S3 对象存储 | 代码就绪，未配置 | 需设置 `PPTS_OBJECT_BACKEND=s3` + 相应 S3 参数 |
| 真实 TTS 配音 | 代码就绪，未配置 | 需设置 `PPTS_TTS_PROVIDER` 及相关 endpoint/key |
| 多租户部署 | RLS 已实现 | 需配置独立 PostgreSQL schema + 域名路由 |
| 导出音频 PPTX | 前端入口存在 | 后端需独立门禁（高成本操作） |

---

*文档自动生成，对应代码版本：7d2f89f-dirty*
