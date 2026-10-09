# PPT 智能语音讲解平台（ppts）

> 把一份静态的 PPTX，变成**会自己开口讲解**的作品：AI 讲稿 + 正式 TTS 配音 + 页面同步字幕，
> 产出可试听、可逐页修改、可导出成视频的完整讲解工程。

🌐 [English](README.md) | **简体中文**

![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)
![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)
[![CI](https://github.com/F31/ppts/actions/workflows/ci.yml/badge.svg)](https://github.com/F31/ppts/actions/workflows/ci.yml)

![讲解页：页面导览 + 三种讲稿模式](assets/demo/ppts1.png)

![讲稿编辑与配音播放：片段级编辑、播放器带时间轴与字幕](assets/demo/ppts2.png)

---

## 目录

- [这是什么](#这是什么)
- [核心优势](#核心优势)
- [功能详览](#功能详览)
- [快速开始](#快速开始)
- [部署](#部署)
- [架构](#架构)
- [配置](#配置)
- [可观测性](#可观测性)
- [安全](#安全)
- [从源码开发](#从源码开发)
- [文档索引](#文档索引)
- [贡献与许可](#贡献与许可)

---

## 这是什么

ppts 解决的是一个朴素但普遍的痛点：**PPT 做完了，但没人讲**。

> 发给客户的方案没人认真翻；给新人做的培训材料你要讲二十遍；想把 PPT 发到视频号、播客或无障碍场景，
> 却发现还得录屏、配音、剪字幕——成本高到干脆放弃。

ppts 把这条链路整个收进来，形成一条闭环流水线：

```text
上传 PPTX  ──▶  解析页面/元素  ──▶  生成 AI 讲稿  ──▶  正式 TTS 配音  ──▶  字幕与时间轴对齐
                    │                                                        │
                    └──────  可选页面渲染（LibreOffice → PDF → PNG）          ▼
                                                              导出：Web 讲解工程 / MP4 / SRT / VTT
```

**适用人群**

- 内容运营、培训、产品团队：批量把既有 PPT 变成可听可看的作品；
- 需要 PPT 视频化 / 本地化的场景：导出 SRT / VTT 直接进剪辑软件；
- Go 开发者：整套能力以 Connect RPC 与 REST 端点开放，可嵌进自有平台。

**技术底座**：解析 PPTX 的能力来自本项目配套的纯 Go SDK [go-pptx](https://github.com/F31/go-pptx)（v2.0.3），
TTS 文本规范化由独立通用组件 [textnorm](https://github.com/F31/textnorm) 承担。三者构成「SDK → 组件 → 平台」三层。

---

## 核心优势

| # | 优势 | 说明 |
|---|---|---|
| 1 | **一条流水线，不是工具拼盘** | 上传、解析、讲稿、配音、字幕、导出一体化；同一套租户与任务状态贯穿全链路 |
| 2 | **AI 讲稿可控** | LLM 生成后经确定性实体校验器检查数字/单位/日期/型号，漂移则最多定向修正一次，仍漂移就**回退原文**——宁可不用 AI，也不产出未经批准的数字改写 |
| 3 | **来源可追溯** | 每段讲稿带结构化来源锚点（来自哪个形状 / 备注 / 页面图），服务端保留锚点、拒绝客户端伪造 |
| 4 | **成本可优化** | 分段音频按内容寻址缓存，相同合成配置跨页面、跨项目复用，零 TTS 调用、零重复存储 |
| 5 | **部署形态随业务伸缩** | 同一个二进制：SQLite 模式零配置开箱即用，PostgreSQL 模式即为完整多租户 SaaS |
| 6 | **可运营** | 多租户与五级角色、配额与计费分账（用户价 vs 供应商成本）、审计留痕、数据导出与彻底擦除 |
| 7 | **工程质量背书** | 真实 PostgreSQL 的 HTTP 端到端测试（真 worker、真存储）、100 页评测语料 + AI 实体校验门禁、前端五道自动门禁 |

---

## 功能详览

### 内容与讲稿

- **三种讲稿模式**：原文阅读 / 润色讲解（书面语转口语）/ AI 生成讲解（补充背景与解释）
- **片段级编辑**：缩短、润色、衔接、**发音**（租户级发音词典，解决 CUDA / MySQL / Kubernetes 等专有名词误读）、停顿
- **乐观并发**：编辑保存带 `expected_revision`，冲突时后端返回最新版本，前端加载最新版并提示
- **来源锚点**：结构通道（`shape_*`，置信度 1.0）+ 视觉通道（`visual_*`，低置信待审）

### 配音与导出

- **正式 TTS 供应商**：默认 SiliconFlow（CosyVoice2），OpenAI 兼容接口，换芯只改环境变量
- **分段级重生成**：只重跑改过的分段，未改分段命中缓存，零 TTS 开销
- **目标时长闭环**：首轮合成后计算速率偏差，超出 ±10% 自动按比例调语率重合成
- **导出格式**：Web 讲解工程（zip：时间轴 + 字幕 + 音频片段）/ MP4 / SRT / VTT
- **页面图渲染**：LibreOffice → PDF → PNG，可选增强；渲染失败只记步骤失败，不影响解析成功

### 平台能力

- **多租户**：PostgreSQL 行级安全（FORCE RLS）+ 五级角色（Owner / Admin / Editor / Reviewer / Viewer）
- **模型网关**：TTS / LLM 的 endpoint、模型、音色在 Web 端配置，租户级与平台级两层
- **配额与计费**：按 token / 生成秒数计量，账本幂等，**供应商成本与用户计费分账**
- **审计与合规**：操作审计（覆盖矩阵由源码 AST 提取并配门禁）、到期归档对象存储、租户数据全量导出与彻底擦除
- **存储三后端**：本地 / S3 兼容 / 租户自带凭据（BYOS），可选服务端信封加密

### Connect RPC 服务

| 服务 | 职责 |
|---|---|
| `ProjectService` | 项目创建 / 读取 / 列表 / 归档 / 页面清单 |
| `UploadService` | 授权直传：创建 / 完成 / 中止上传 |
| `ScriptService` | 讲稿读取 / 更新 / 审核 / 锁定 / 草稿生成 |
| `NarrationService` | 创建配音生成任务 |
| `PlaybackService` | 获取配音时间轴与播放 manifest（音频 + 字幕 + 页面图） |
| `ExportService` | 创建导出 / 读取产物 / 创建下载链接 |
| `JobService` | 任务查询、取消、重试与事件流 |
| `TenantService` | 成员角色、用量、审计、导出与擦除 |

---

## 快速开始

### 方式一：Docker Compose（推荐先试）

```bash
git clone https://github.com/F31/ppts.git && cd ppts
cd web && npm ci && npm run build && cd ..   # 前端产物会被打进 api 镜像
docker compose build
docker compose up -d
# API + 控制台：http://localhost  （健康检查 http://localhost/healthz）
```

> 自建编排时注意：api 要占用 80 特权端口，`docker-compose.yml` 里的 api 服务带了 `cap_add: [NET_BIND_SERVICE]`。
> **漏掉这一项，进程会在 bind 时报 `EACCES` 直接退出。**

镜像：api 约 **168MB**（Go 单进程同时服务 API 与前端静态资源，无需 nginx），worker 约 **820MB**（含 LibreOffice、poppler、中文字幕字体与静态 ffmpeg）。两者均以 uid 10001 非 root 运行。

### 方式二：单二进制（SQLite 零配置）

```bash
scripts/package.sh [版本号]        # 缺省取 git describe
tar xzf dist/ppts-<版本>-linux-amd64.tar.gz && cd ppts-<版本>-linux-amd64
sudo ./install.sh                  # 装依赖 → 建账号 → 生成配置 → 建库迁移 → systemd 启动
```

发行包内含：单一入口二进制 `ppts`（`CGO_ENABLED=0` 纯静态）、静态 ffmpeg/ffprobe、`install.sh`、systemd unit 与配置模板。
**前端产物与 SQL 迁移均已内嵌进二进制**，单文件即可部署。

四个子命令：

| 命令 | 说明 |
|---|---|
| `ppts server` | API + 前端控制台（默认监听 `:8080`，由 `PPTS_HTTP_ADDR` 覆盖） |
| `ppts worker` | 任务执行器（解析 / 讲稿 / 配音 / 导出） |
| `ppts migrate` | 幂等应用内嵌迁移 |
| `ppts version` | 打印版本 |

最简本地起服（SQLite 默认，数据落 `~/.ppts/ppts.db`，无登录）：

```bash
PPTS_OBJECT_BACKEND=local PPTS_OBJECT_ROOT=./var/ppts-objects \
PPTS_OBJECT_SECRET="$(head -c32 /dev/urandom | base64)" \
GOWORK=off go run ./cmd/ppts server
```

打开 <http://localhost:8080> 即可。

### 方式三：PostgreSQL 生产模式

```bash
PPTS_DATABASE_URL='postgres://ppts_app:...@host:5432/ppts' \
PPTS_OBJECT_ROOT=./var/ppts-objects \
PPTS_OBJECT_SECRET='dev-download-secret' \
GOWORK=off go run ./cmd/ppts server

PPTS_DATABASE_URL='postgres://ppts_app:...@host:5432/ppts' \
PPTS_TENANT_ID='00000000-0000-0000-0000-000000000000' \
PPTS_TTS_PROVIDER='fake' \
GOWORK=off go run ./cmd/ppts worker
```

> **数据库账号必须是非表 owner 的受限角色**。迁移由 `ppts_migrator` 持有 owner 权限执行，
> 运行时账号 `ppts_app` 必须 `NOSUPERUSER NOBYPASSRLS`，否则行级安全会被绕过。详见 [安全](#安全)。

---

## 部署

### 安装脚本

`install.sh` **幂等可重复执行**，不覆盖既有配置。

```bash
SKIP_DEPS=1  sudo ./install.sh     # 跳过系统依赖安装
SKIP_START=1 sudo ./install.sh     # 只安装不启动
```

布局：`/opt/ppts/bin`、`/etc/ppts/config.env`、`/var/lib/ppts`（本地对象存储与 LibreOffice profile）。
systemd unit 见 `packaging/ppts-api.service` 与 `packaging/ppts-worker.service`。

> ⚠️ 随包 ffmpeg 为静态构建，含 GPL 组件（x264 / libass）。**商业分发时请确认许可证合规。**

### 外部命令依赖

| 能力 | 依赖 | 缺失时的行为 |
|---|---|---|
| 页面图渲染 | `soffice`（LibreOffice）、`pdftoppm` / `pdfinfo`（poppler） | 渲染作为可选增强失败，解析照常成功，产物可能缺页面图 |
| 音视频合成 / MP4 导出 | `ffmpeg` / `ffprobe` | 对应导出格式不可用 |

二进制路径可用 `PPTS_SOFFICE_BIN` / `PPTS_PDFTOPPPM_BIN` / `PPTS_PDFINFO_BIN` 覆盖。
在 Windows 上经 WSL interop 调用 soffice 时，`PPTS_RENDER_WORK_ROOT` 必须指向 `/mnt/<drive>` 下的路径。

---

## 架构

```text
┌──────────────────────────────────────────────────────────────┐
│  控制台（React + Vite + TypeScript）                          │
│  OIDC PKCE 登录 / 项目 / 讲解 / 任务中心 / 系统设置           │
└───────────────────────────────┬──────────────────────────────┘
                                │ Connect RPC + REST
┌───────────────────────────────▼──────────────────────────────┐
│  API（cmd/ppts server）                                       │
│  8 个 Connect 服务 · 模型网关 · 对象签名端点 · 审计           │
└───────────────┬───────────────────────────────┬───────────────┘
                │ 任务入队                       │ 读写
┌───────────────▼───────────────┐   ┌───────────▼───────────────┐
│  Worker（cmd/ppts worker）     │   │  存储                      │
│  解析 / 讲稿 / 配音 / 导出     │   │  SQLite 或 PostgreSQL(RLS) │
│  租约 · 重试 · 取消 · 进度上报 │   │  对象：local / S3 / BYOS    │
│  保留清理 · 孤儿扫描 · 归档    │   │                            │
└───────────────┬───────────────┘   └───────────────────────────┘
                │
        ┌───────┴────────┬──────────────┬──────────────┐
        ▼                ▼              ▼              ▼
    go-pptx          textnorm       LLM 供应商      TTS 供应商
  (PPTX 读/写)   (文本规范化)     (OpenAI 兼容)   (OpenAI 兼容)
```

### 任务流水线的可靠性语义

- **任务状态 / 重试 / 租约 / 取消**：统一由 `internal/pipeline` 管理；`ppts_claim_next_job` 是受限函数，调度账号无业务表通用读权限
- **进度上报**：worker 注入 `pipeline.ReportProgress`，handler 调用即带 fencing 条件更新，配音按已合成分段上报 0–100%
- **outbox**：`SetCommitStep` 声明「完成后才存在的最终产物步骤」，worker 在 `CompleteWithStep` 中与任务终态**同一事务**原子 upsert——不存在「步骤成功但任务没终态」的崩溃窗口
- **事件流**：`WatchEvents` 基于专用事件表的单调 seq，客户端用 `after_seq` 断点续传（不复用时间戳，避免同毫秒并发漏发）

### 目录结构

```text
cmd/ppts            单一入口：server / worker / migrate / version
cmd/local-engine    桌面 Go sidecar
internal/project    源文件版本 / 页面 / 元素 / 导入报告 + go-pptx 读适配器
internal/narration  讲稿 / 分段 / 来源锚点 / 术语 / 审核
internal/pipeline   任务状态 / 重试 / 租约 / 取消 / 事件 / outbox
internal/media      音轨 / 字幕 / 时间轴 / 导出
internal/tenant     身份 / 角色 / 策略 / RLS
internal/usage      额度 / 结算 / 账本 / 成本
internal/integrations  LLM / TTS / 渲染 / 存储适配
proto/ppts/v1       Connect 契约（8 个服务）
migrations          SQL 迁移（expand → migrate → contract）
web/                前端工作区（Vite + React + TypeScript）
docs/               技术方案 / ADR / Runbook / 跟踪计划
```

---

## 配置

所有配置均为环境变量（或随包生成的 `/etc/ppts/config.env`）。最常用：

| 变量 | 说明 |
|---|---|
| `PPTS_DB_DRIVER` | `sqlite`（默认，单租户轻量）或 `postgres`（多租户全功能） |
| `PPTS_DATABASE_URL` | PostgreSQL 运行时连接串（**须为非 owner 的受限账号**） |
| `PPTS_MIGRATE_DATABASE_URL` | 迁移用 DSN（需较高权限，回退 `PPTS_DATABASE_URL`） |
| `PPTS_HTTP_ADDR` | API 监听地址，默认 `:8080` |
| `PPTS_OBJECT_BACKEND` | `local`（默认）/ `s3` / `byos:<credential_id>` |
| `PPTS_OBJECT_ROOT` | 本地对象存储根目录 |
| `PPTS_OBJECT_SECRET` | 本地签名链接密钥 |
| `PPTS_TTS_PROVIDER` | `fake`（开发）/ `siliconflow`（正式） |
| `PPTS_LLM_PROVIDER` | `siliconflow`（当前支持 OpenAI 兼容接口） |
| `PPTS_GATEWAY_AES_KEY_BASE64` | 模型网关凭据加密密钥 |
| `PPTS_METRICS_TOKEN` | `/metrics` 与 `/debug/vars` 的 Bearer token（**未配置一律拒绝**） |
| `PPTS_OTEL_EXPORTER_OTLP_ENDPOINT` | 设置后经 OTLP 导出链路，未设置则 no-op |

### 身份与登录

- SQLite 模式无登录（固定本地租户 / 用户），开箱即用
- PostgreSQL 模式支持正式 OIDC：设置 `PPTS_OIDC_ISSUER` / `PPTS_OIDC_CLIENT_ID` 后，API 经 issuer discovery + JWKS 验签，默认从 `tenant_id` 与 `sub` claim 推导租户与用户（可用 `PPTS_OIDC_TENANT_CLAIM` / `PPTS_OIDC_USER_CLAIM` 覆盖）
- 配置 OIDC 后**默认拒绝**开发身份头；仅本地开发可显式 `PPTS_AUTH_DEV_HEADERS=true` 放行 `X-PPTS-Tenant-ID` / `X-PPTS-User-ID`
- Web 侧 OIDC PKCE 用 Vite 环境变量：`VITE_OIDC_AUTHORIZATION_ENDPOINT` / `VITE_OIDC_TOKEN_ENDPOINT` / `VITE_OIDC_CLIENT_ID`

### TTS / LLM 供应商

两者均走 OpenAI 兼容接口，可通过 Web 顶栏「模型网关」配置，**优先级为「租户默认 → 平台默认 → 环境变量」**，运行时解析结果缓存 30s。
网关里的 API Key 以 AES-GCM 加密落库，读接口只返回 `hasKey` / `keyMasked`。

| 供应商 | 默认模型 | 备注 |
|---|---|---|
| TTS `siliconflow` | `FunAudioLLM/CosyVoice2-0.5B` | voice 约定 `<model>:<voice>`；429 / 5xx 映射为可重试 |
| LLM `siliconflow` | `Qwen/Qwen2.5-7B-Instruct` | 视觉模型默认 `Qwen/Qwen3-VL-8B-Instruct`，用于页面图理解 |
| TTS `fake` | — | 只生成静音 WAV，**不构成正式供应商验收** |

### 存储

- `local`：本地目录 + 服务端签名链接
- `s3`：S3 兼容，租户 `policy.storage_region` 可按区域路由到 `{bucket}-{region}`（懒创建并缓存）
- `byos:<credential_id>`：租户自带凭据，AES-GCM 密文落库，AAD 绑定 tenant / credential / backend
- **信封加密**：设置 `PPTS_OBJECT_ENCRYPTION_KEY_BASE64` 后，开启该策略的租户对象在服务端加/解密（启用后直接预签名读写返回不支持）
- 生命周期由租户策略驱动（`storage_transition_days` / `storage_expiration_days`）；源对象保留、派生档过期、上传废弃与额度预占回收由 worker 周期执行
- **孤儿对象扫描默认只报告不删除**（写 `orphan.detected` 审计）；确认候选无误后再设 `PPTS_ORPHAN_DELETE=1` 才真删——删对象不可逆，而「无引用」是启发式判定

---

## 可观测性

- **结构化日志**：slog JSON，每请求带 `X-Request-ID`，输出 `request_id / method / path / status / duration_ms / tenant / user`
- **链路追踪**：API 经 Connect interceptor 生成 span，创建任务时把 W3C `traceparent` 持久化到 `jobs.traceparent`，worker 以 SpanLink 关联，**实现跨进程异步任务链路**；未配 OTLP 则零依赖 no-op
- **指标**：`/metrics` 需 Bearer `$PPTS_METRICS_TOKEN`；默认只按 kind × event 聚合以避免高基数，排障时可开 `PPTS_METRICS_TENANT_LABELS=true`
- **队列视图**：按任务类型拆分的 depth / running / backoff / oldest-wait 指标（多副本读同一份表，不是各自记一份）。**「没出现过」与「当前为 0」必须可区分**——未出现的 kind 不会有键；判断某个 kind 是否还在用，看 `ppts_worker_jobs_total` 的计数，别看 depth gauge
- **降级态指示器**（出现即需处理，不是可选优化）：

  | 指标 | 含义 |
  |---|---|
  | `ppts_tts_fake_provider_active` | 假 TTS 在用 → 产出的是伪造音频 |
  | `ppts_render_pages_disabled` | 渲染禁用 → 产物可能缺页面图 |
  | `ppts_auth_dev_headers_active` | 开发身份头放行 → 身份可被伪造，生产不得出现 |
  | `ppts_ratelimit_degraded=1` | 限流降级为单副本计数 → 实际放行量 = 限额 × 副本数 |

- **健康检查**：`GET /healthz` 真正探测数据库与对象存储，任一不可用即 503。响应**只含分项状态、不含错误原因**（该端点免鉴权，错误原文只进服务端日志）；后端无法廉价探活时报 `unsupported`，表示「没查」而非健康
- SLO 与告警规则见 `docs/runbooks/SLO与告警.md`

---

## 安全

- **行级安全**：迁移由 owner 角色执行，运行时账号必须 `NOSUPERUSER NOBYPASSRLS`，租户业务表 `FORCE ROW LEVEL SECURITY`，策略依据事务局部 `app.tenant_id`；缺失上下文时访问被拒绝
- **失败即关闭**：`/metrics`、`/debug/vars` 未配置 token 一律拒绝（忘配 = 拒绝而非裸奔）；`/debug/pprof/*` 默认完全不挂载，需 `PPTS_PPROF=true` 且同样过鉴权
- **密钥处理**：网关凭据、BYOS 凭据、对象信封加密密钥均为 AES-GCM；审计默认不写入 token / publicId / IP / UA（避免把短期凭据复制进更长寿的留存）
- **审计**：`audit_events` 按租户隔离并留痕；到期归档为 JSONL 存入对象存储
- **数据合规**：`ExportTenant` / `PurgeTenant`（owner 权限）支持租户数据全量导出与彻底擦除
- 角色定义见 `internal/api/roles.go`，高危操作覆盖矩阵见 `docs/审计覆盖矩阵.md`

---

## 从源码开发

> 仓库内含 `go.work`（挂接 `../go-pptx` 与本地 `./textnorm` 组件）。CI 与发布用 `GOWORK=off`。

### 后端

```bash
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...

# 含 PostgreSQL 的测试（共享测试库，必须串行）
PPTS_TEST_DATABASE='postgres://.../ppts_test' GOWORK=off go test -tags=pg -p 1 ./... -count=1
```

> **读测试输出的两条口径**：
> ① 默认 `go test ./...` **不含** PG 用例——它们由 `-tags=pg` 打开，CI 的 `postgres` job 才跑这一支，
> 所以「ok 包数」不等于全部用例都执行过；
> ② 依赖外部工具 / 凭据（ffmpeg、poppler、S3、真实音频）的用例一律 `t.Skip` 而非失败，
> **SKIP 计数必须与 ok / FAIL 一起看**——SKIP 变多往往意味着环境缺了什么，而不是运气变好。

### 前端

```bash
cd web
npm ci
npm run build       # = contrast → api:check → route:check → core:test → polling:test → tsc -b → vite build
npm run i18n:check  # 不进 build，需单独执行；CI 的 web job 已单独挂一步
```

门禁含义：`contrast` 对比度契约、`api:check` 请求出口契约、`route:check` 路由清单、`core:test` / `polling:test` 单元行为测试、`i18n:check` 中英文不对等与孤儿 key。

### 评测与运维脚本

| 脚本 | 用途 |
|---|---|
| `scripts/gen_corpus` | 生成 13 套共 100 页语料（含数字 / 单位 / 型号实体） |
| `scripts/ai_eval` | AI 评测门禁：逐页 LLM 润色后用实体校验器检查关键数字是否忠于来源，**任一页漂移即失败** |
| `scripts/replay_eval` | 校验规则回放门禁（不改写；相对基线零翻转） |
| `scripts/audit_coverage` | 从源码 AST 生成 `docs/审计覆盖矩阵.md`（勿手工编辑） |
| `scripts/backup_restore_drill.sh` | 备份 / 恢复演练 |

---

## 文档索引

> 注：以下文档位于本地仓库 `docs/` 目录，**不随 GitHub 分发**。
> 克隆仓库后即可查阅，路径均相对仓库根目录。

| 文档 | 说明 |
|---|---|
| `docs/PPTS功能与API说明书.md` | 功能与接口说明 |
| `docs/PPT自动讲解工具-技术方案-V4.0.md` | 当前技术基线方案 |
| `docs/SQLite单租户部署.md` | SQLite 模式能力边界与配置 |
| `docs/runbooks/SLO与告警.md` | 告警规则与降级处置 |
| `docs/审计覆盖矩阵.md` | 由脚本生成的高危操作覆盖矩阵 |
| `docs/adr/` | 架构决策记录 |
| `docs/PPT智能语音讲解平台-控制台产品优化实施计划-V1.0.md` | 控制台演进计划 |

---

## 贡献与许可

欢迎 issue 与 PR。提交前请跑通上一节的后端与前端验证回路；涉及请求出口、路由、i18n 的改动需保证对应门禁通过。

相关项目：

- [go-pptx](https://github.com/F31/go-pptx) —— 本项目使用的纯 Go PPTX SDK（Apache-2.0）
- [textnorm](https://github.com/F31/textnorm) —— TTS 文本规范化引擎（独立通用组件）

**许可证：Apache License 2.0**（见 [LICENSE](LICENSE)）。

联系方式：jinfeng105@126.com / jinfeng105@gmail.com
