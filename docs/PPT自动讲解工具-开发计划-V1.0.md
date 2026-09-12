# PPT 自动讲解工具 — 项目开发计划 V1.1

> 版本：V1.1｜编制日期：2026-09-12
> 基线：已确认的《PPT自动讲解工具-技术方案-V4.0.md》（在 V3.6 基础上新增：桌面隐私模式 §11.10、云端处理合理性 §13.1、对象存储隔离与多后端 §12.4、存储成本 §12.5、向量检索按需 §6.6，及 ADR-014/015/016）。本文只做排期、任务分解、资源与门禁，不重述技术判断。
> 项目根目录：`E:\projects\ppts`（WSL 下为 `/mnt/e/projects/ppts`）。
> 代码仓库：`https://github.com/F31/ppts.git`（ppts 主仓库）；文档适配组件 go-pptx 已发布 v1.0.0（`github.com/F31/go-pptx`，模块依赖，开发期经 `go.work` 复用本地产库 `E:\projects\go-pptx`）。
> 假设：一名 Go 后端、一名前端、兼职测试/产品支持；原生阶段另需 Rust 与移动音频能力。单人开发需重新排期（见 §6）。
> 不确定项：go-pptx 渲染/配音两缺口、复杂 PPT 兼容、本地模型/能力包部署（详见 §4.1 与 §6）。

## 1.1.0 go-pptx 已验证能力基线（v1.0.0，2026-09-11）

go-pptx 已不是"待验证依赖"（V3.6/V4.0 §证据边界 表述均已被 v1.0.0 发布取代）。以官方 v1.0.0 能力矩阵为准：

| 维度 | 状态 | 对 ppts 的影响 |
|---|---|---|
| Inspect | Supported（只读 IR、页序/形状/表格/图表/几何/字体/版式/嵌入字体报告） | G0-1 解析能力基本闭环；图表嵌入数据可读 |
| Edit | Partial（跨 Run 文本替换、属性 patch、受限播放/过渡/绑定/复制） | 讲稿编辑不需要，读路径够用 |
| Create | Partial（文本框/自选图形/图片/图表/音视频/页面创建） | 带音频 PPTX 的基础部件可用 |
| Preserve | Supported（字节级保真 B1、原子落盘、保存报告） | 源文件不可变 + 只改副本有保障 |
| Play | **Partial（配音受限 + timing 只读透传）** | **带音频 PPTX（P 级）仍是独立门禁**，见 G0-8 |
| Render | **Untested（设计态推迟）** | **渲染必须走 LibreOffice→PDF→PNG 基线**，见 G0-2 |

验证证据：36 份真实样本 + 3 份公开 LibreOffice 金样；PowerPoint 16.0.20326 / WPS 12.1.0.28599 真机 8/8 无修复提示（`docs/client-compat-matrix.md`）；覆盖 83.2%；Apache-2.0。依赖集成方式与版本锁定见 §3。

## 1. 目标与边界

### 1.1 首个可用产品（P0 范围）

| 项 | 内容 |
|---|---|
| 输入 | 中文 PPTX（≤100页、≤100MiB），页序读取、备注/正文提取、隐藏页提示、异常页报告 |
| 讲稿 | 三种模式（原文朗读 / 润色讲解 / AI 生成讲解）、逐页编辑、草稿审核、锁定、撤销、revision 冲突检测 |
| 语音 | 一家正式 TTS（Azure 为基线，区域/采购不合适时同语料测国内正式 API）、音色试听、语速、分段停顿、局部重生成 |
| 播放 | 音频驱动切页、暂停/继续、跳页、倍速、字幕开关、键盘操作；媒体时钟为主时钟 |
| 导出 | 静态画面 MP4、音频包、SRT/VTT；带音频 PPTX 为独立开发门禁（G0 验证） |
| 工程 | 统一工程模型（slide/segment/audio/alignment/timeline）、不可变版本、增量失效 |
| 后台 | PostgreSQL 任务表 + 短事务领取 + 租约 + fencing、幂等提交、额度预占、用量账本、RLS |
| 存储 | `ObjectStore` 端口（V4.0 §12.4）+ 租户/项目前缀键结构 + 本地/S3 适配器；签名链接校验租户上下文 |
| 文档适配 | go-pptx v1.0.0（Inspect/Create/Edit/Preserve 已验证；渲染走 LibreOffice 基线；配音受限为独立门禁） |
| 多端 | Web（独立部署）+ Tauri 2 桌面/移动共享 React；桌面按需 Go sidecar；移动云端重任务 + L1/L2 离线 |

### 1.2 明确暂缓（不进入本轮里程碑）

- 不建设 Wails、不建独立 Flutter 工程
- 不自研完整 Office 渲染引擎；动画按清单（P/A 级），不宣称任意动画一致
- 多 Agent、**独立部署的向量数据库**、**本地 LLM 推理**不作为基础依赖（V4.0 §16.2）
- 向量检索不作为默认基础设施：关键词/结构化检索用 PostgreSQL `tsvector`/GIN；语义问答检索仅在"暂停提问"立项时按需引入 pgvector，不新增独立向量数据库（V4.0 §6.6）
- 不预建 MySQL 适配，不默认引入 Kubernetes/Redis/NATS/JetStream
- 移动端 L3 离线生成、声音克隆、SSO 高级集成、实时协同（进入 P2/G5）
- 桌面隐私模式仅桌面提供，移动端不提供、文案不暗示同等能力（V4.0 §11.10）

### 1.3 V4.0 增量对计划的映射

| V4.0 增量 | 方案章节/ADR | 本计划落点 |
|---|---|---|
| 桌面隐私模式 | §11.10 / ADR-015 | G4-9（开关+能力清单）；完整离线依赖 G5 本地能力包 |
| 云端处理合理性 + 缓解措施 | §13.1 | G1-1（处理后删除选项/诚实文案/保留期字段）、G3-7（保留期与清理）、G3-6（区域选择） |
| 对象存储隔离 | §12.4 / ADR-014 | G0-9（端口+本地适配器+键结构）、G1-9（S3适配器+签名校验）、G3-6（多后端/BYOS/加密） |
| 存储成本与生命周期 | §12.5 | G2-7（内容哈希去重）、G3-6（生命周期分层+成本可见性） |
| 向量检索按需 | §6.6 / ADR-016 | P1 全文检索用 tsvector（§1.2）；语义检索仅当"暂停提问"立项，pgvector 按需引入 G5 |

## 2. 里程碑总览

阶段沿用 V4.0 §16，G4 为串行依赖；时间以 2026-09-14 起算，留 20% 缓冲。

| 阶段 | 窗口 | 交付物 | 并行轨道 |
|---|---|---|---|
| G0 技术验证 | 2–3周（09/14–10/02） | go-pptx 集成与渲染/配音门禁；`ObjectStore` 端口与本地适配器；Tauri 桌面 Go 调用；Android/iOS 文件导入与后台音频原型 | 后端 / 前端 / 原生三轨并行 |
| G1 基础闭环 | 3–4周（10/05–10/30） | Web 编辑、源文件版本、持久化任务、逐页配音、播放器、MP4/字幕、对象存储键与签名 | 后端 + 前端主轨 |
| G2 AI 产品化 | 2–3周（11/02–11/20） | 三种稿件模式、来源校验、读音词典、局部重生成、预算、内容哈希去重 | 后端 AI 轨 + 前端编辑轨 |
| G3 商用加固 | 2–3周（11/23–12/11） | RLS、配额账本、角色、审计、备份与故障演练、多后端存储/生命周期/保留期 | 后端为主，测试全程 |
| G4 统一原生客户端 | 3–5周（12/14–01/15） | Tauri 桌面/移动、共享适配器、桌面 sidecar、移动 L1/L2 与原生播放、桌面隐私模式开关 | 原生轨 + 共享前端轨 |
| G5 能力扩展 | 独立估算 | 桌面 L3、数字人、动画、多语言、SSO 高级集成、本地能力包、pgvector 按需 | 按市场反馈另行立项 |

> 门禁约定：G0–G3 期间仅交付 Web/MP4 完整闭环；配音 PPTX 未过 G0 门禁则保持实验状态，不得用损坏文件完成里程碑（V3.6 §16）。

## 3. 工程基础建设（G0 先行，贯穿全程）

启动时一次性搭好，避免各阶段返工：

1. 仓库与依赖：`git init` 关联 `https://github.com/F31/ppts.git`；`go.mod` 依赖 `github.com/F31/go-pptx v1.0.0`；开发期用 `go.work` 指向本地产库（`E:\projects\go-pptx`），发布锁定正式 tag（V4.0 §15.1 原则）。
2. 仓库结构：`cmd/api、cmd/worker、cmd/local-engine、internal/{project,narration,pipeline,media,tenant,usage,integrations}、proto、migrations、web、apps/native/src-tauri、packages/{ui,editor,platform,api-client}、testdata、docs/adr`（V4.0 §15.1）。存储适配器置于 `internal/integrations/objectstore/`（本地/S3/BYOS），`ObjectStore` 端口定义在使用它的模块附近。
3. 契约：Protobuf + Buf lint/breaking + Go/TypeScript 生成客户端；先定 ProjectService/UploadService/ScriptService/NarrationService/JobService/ExportService/TenantService 的方法与错误模型。
4. 数据：pgx + sqlc；多租户字段、授权上下文、持久化任务与账本唯一性从 G1 建模（V4.0 §16）。
5. 迁移：expand → migrate → contract；单独发布步骤执行；任务 payload/工程 schema/Prompt/词典/模型/渲染器/字体/存储后端均记录版本。
6. CI：契约兼容检查、回归语料跑批、依赖检查（业务域不依赖云 SDK/HTTP 框架，`ObjectStore` 由适配器实现）、并发安全测试。
7. 观测：结构化日志、基础指标；租户/项目 ID 入日志与追踪，不做高基数标签；存储占用按租户汇入"每项目成本"指标（V4.0 §12.5）。
8. 存储端口：G0 即定义 `ObjectStore` 接口（Put/Get/SignedURL/Delete/ApplyLifecyclePolicy，V4.0 §12.4），业务代码只依赖端口，不直接引用云 SDK。

## 4. 各阶段任务分解

### 4.1 G0 技术验证（2–3周）— 消解剩余不确定项（go-pptx 主体已 v1.0 验证）

| 编号 | 任务 | 归属 | 验收 |
|---|---|---|---|
| G0-1 | go-pptx 集成：读路径（页序/备注/正文/表格/图表）进 `internal/project` 适配器；写路径（媒体/ContentTypes/relationships/timing/transition）进 `internal/integrations` 适配器；能力缺口用 OOXML 补丁或商业 SDK 兜底，接口保持稳定 | 后端 | go-pptx v1.0.0 能力基线对照表落地；UNSUPPORTED_FEATURE 显式返回，无伪成功 |
| G0-2 | 渲染基线验证：LibreOffice → PDF → 逐页 PNG；固定字体/版本；动画与字体替换逐页报告。**决策（2026-09-12）**：LibreOffice 为最终渲染路径，商业 SDK 仅作可选替换适配器、不并行验证；PDF→PNG 光栅化可选 go-pdfium/Ghostscript 替换 poppler 减依赖；渲染只进 worker 容器，桌面 L3 作为能力包 | 后端 | 认证语料渲染报告 |
| G0-3 | 静态 MP4 最小链路：PPTX → 页面图 → FFmpeg 合成；ffprobe 与抽帧校验 | 后端 | 1080p/H.264/AAC 样例通过 |
| G0-4 | Tauri 桌面壳 + Go sidecar 握手/有界 JSON IPC（request/response/Progress） | 原生 | 协议版本校验、半条消息、取消、强杀恢复 |
| G0-5 | Android/iOS：文件导入（系统授权 URI）、后台音频播放原型 | 原生 | 真机锁屏播放/耳机/来电中断记录 |
| G0-6 | 兼容性语料库首建：≥10 份中英混排/表格/图表/隐藏页/嵌入媒体样例（可复用 go-pptx 语料） | 测试 | 语料登记与预期清单 |
| G0-7 | 默认四步流程静态原型（上传 → 配置 → 试听 → 导出） | 前端 | 交互走查通过 |
| G0-8 | 带音频 PPTX 门禁：go-pptx Play 为 Partial，验证 AddAudio + timing + transition（advTm/advClick）+ p:timing 保留；未过则保持实验状态，先交付 Web/MP4 | 后端 | 认证客户端（PowerPoint 版本登记）自动播音、切页不串音、重开保存 |
| G0-9 | 存储最小验证：`ObjectStore` 端口 + 本地文件适配器 + 键结构约定 `{tenant_id}/{project_id}/{rev|artifact}/{type}/{id}.{ext}`；预签名 URL 校验租户上下文与键前缀（V4.0 §12.4） | 后端 | 本地适配器读写/签名/越权前缀拒绝；键结构单测 |

**放行门禁**：确认渲染、TTS、原生音频、存储四条路径；不支持能力有替代路径；TTS/供应商选择有结论。

**G0 实测进度（2026-09-12 登记）**：

| 任务 | 状态 | 证据 |
|---|---|---|
| G0-1 读适配器 | ✅ 已实现 | `internal/project`，reader 测试 3 项 + 语料回放 11 项 |
| G0-1 写适配器（配音） | ✅ 已实现 | `internal/integrations` NarrationWriter，回读/Validate/幂等测试 |
| G0-2 渲染 | 🟡 代码完备，本机待装 LibreOffice | `internal/integrations/render`；poppler 链路实测通过（`TestPopplerRasterize`），soffice 全链路测试待有环境机自动启用 |
| G0-3 MP4 | ✅ 实测通过 | `internal/media` ffmpeg 编码 + ffprobe + 抽帧（h264/aac/等比留边） |
| G0-4 sidecar IPC | ✅ Go 侧已实现 | `internal/sidecar` 握手/能力/超限拒收测试；Rust 桥接在 G4 |
| G0-6 语料 | ✅ 首建 | `testdata/corpus`：8 合成（`scripts/gen_corpus`）+ 3 go-pptx 金样，回放全绿 |
| G0-9 存储 | ✅ 已实现 | `internal/integrations/objectstore`，键/签名/越权/本地适配器测试 |
| G0-5 移动音频 | ⏸ 待真机 | Android/iOS 设备要求，卡 G4 前置；原型环境阻塞项 |
| G0-8 配音 PPTX | ⏸ 待真机 | 需 Windows/PowerPoint 认证书面验证；写链路的回读/Validate 已通过，真机门禁未过 |

> 环境阻塞项处理：G0-5/G0-8 依赖真机/Windows，无法在本环境完成；其代码侧基础（移动播放接口、NarrationWriter）已就绪，真机验证随 G4 一并执行，不再回填 G0。go-pptx 本地产库已推进到 v1.0.1（高于计划记录的 v1.0.0），适配器按其当前 API 编写。TTS 供应商选择待采购/区域核验后登记 ADR。

### 4.2 G1 基础闭环（3–4周）— 可用的 Web 产品

| 编号 | 任务 | 验收 |
|---|---|---|
| G1-1 | 上传：授权直传/分片、CompleteUpload 校验大小/哈希/租户所有权；源文件不可变版本；对象键按租户/项目前缀；项目设置含"处理完成后删除源文件"选项与源文件保留期字段；上传/处理前置文案明确"文件将上传至云端"（V4.0 §13.1） | 校验失败拒绝，异常页报告；键结构无绕过拼接 |
| G1-2 | 解析流水线：go-pptx 适配器 → 页面/元素/备注/来源锚点模型；渲染报告入库 | 页面顺序不依赖文件名排序 |
| G1-3 | 任务系统：数据库任务表、SKIP LOCKED 领取、租约、fencing、崩溃恢复、幂等提交 | 强杀后无任务静默丢失；外部成功崩溃不重复结算 |
| G1-4 | 讲稿编辑：逐页编辑、revision 冲突检测（expected_revision）、锁定、撤销 | 并发冲突返回差异，禁止静默覆盖 |
| G1-5 | 分段 TTS：一家正式供应商、真实时长、时间戳/对齐、分段拼接 | 抽样句首偏差 P95≤200ms（待语料） |
| G1-6 | 播放器：媒体时钟为主时钟、跳页/倍速/字幕、页面图与音轨资源加载 | 30分钟尾部偏差≤100ms（待语料） |
| G1-7 | 导出：MP4 静态画面、音频包、SRT/VTT；统一时间轴装配；下载/分享走短期签名链接 | ffprobe + 抽帧 + 字幕抽验；签名过期/撤销 |
| G1-8 | Web 三栏编辑器 + 自动保存 | 编辑不丢稿 |
| G1-9 | 存储：S3 兼容适配器（开发/试点档 S3/MinIO/本地均可）、对象键生命周期基础（源热存储、临时前缀清理）、预签名校验独立单测 | 本地与 S3 适配器双跑通过；越权前缀拒绝 |

**放行门禁**：重启可恢复、语音与字幕同步、源文件可追溯、对象键租户隔离。

**G1 实测进度（2026-09-12 登记）**：

| 任务 | 状态 | 证据 |
|---|---|---|
| G1-3 任务系统 + worker | ✅ 已实现并实测 | `internal/pipeline`：SKIP LOCKED 领取（LIMIT 1）/租约/fencing/退避重试/取消/幂等/崩溃重领取；11 项 `-tags=pg` 端到端全过 |
| G1-1/1-2 上传+解析垂直链路 | ✅ 已实现并实测 | `internal/app`：IngestService（哈希→对象→源版本→parse 任务，按源哈希幂等）+ ParseHandler（读对象→Inspect→回写 document.json）；2 项 `-tags=pg` 全过 |
| 持久化 | ✅ 仓库层落地 | `internal/project/store.go` ProjectStore（revision 递增与版本行同事务原子）；0001_init.sql 已应用 dev/test 两库 |
| 数据库门控约定 | ✅ | PG 标签测试需 `PPTS_TEST_DATABASE` 且 **`-p 1` 串行执行**（多包共享测试库，并行互 TRUNCATE） |
| G1-4 讲稿编辑 | ✅ 已实现并实测 | `internal/narration`：乐观并发（expected_revision）+ FOR UPDATE 冲突返回最新版与差异；draft→approved→locked 状态机，锁定后拒改；2 项 `-tags=pg` 全过；0002_scripts.sql 已应用 |
| G1-9 存储多后端 | ✅ S3 适配器已实测 | `internal/integrations/objectstore/s3`：minio-go 实现（Put/Get/Delete、预签名读/写、生命周期过期规则）；本地 MinIO（`quay.io/minio/minio`，端口 9000）真实通过 4 项测试；门控 `S3_ENDPOINT` |
| Connect API 入口 | ✅ 主链路已实测 | Buf/Protobuf/Connect Go 绑定已生成并纳入源码；`internal/api` 提供可信身份头注入、Project Create/Get/List/Archive、Script Get/Update/Approve/Lock、Narration CreateGeneration、Playback GetManifest、Export CreateExport/GetArtifact/CreateDownload、健康检查；Project transport 已接 PG store 与 `cmd/api`，HTTP/Connect 端到端测试覆盖 |
| G1-5 分段 TTS | 🟡 本地闭环完成，正式供应商待接 | `internal/integrations/tts`：供应商端口 + FakeProvider 3 项测试；`internal/app/NarrationHandler` 固定多页讲稿 revision、全量预检、逐段配置哈希、不可变音频/对齐 manifest、半写入保护、重跑资产复用与 RetryableError 退避映射，4 项测试全过；缓存身份为 slide+稳定 segment+配置哈希，未改分段可跨讲稿 revision 复用且不同分段不会互相覆盖；`cmd/worker` 已装配 parse/narration 分发。正式供应商与句首偏差 P95 门禁待凭据/语料 |
| G1-6/7 播放器/导出 | 🟡 播放/导出后端闭环已落地 | `internal/media` 以整数微秒实现页面/分段/字幕唯一时间轴；PCM16 WAV 按精确采样位置装配并补静音；MP4 以逐页 loop + CFR concat filter 支持任意页时长并抽帧确认切页。`internal/artifact` + `0003_artifacts.sql` 持久化不可变成品，按 `(tenant, project, snapshot_hash, format)` 幂等；`internal/app/ExportHandler` 支持 SRT/VTT 复制与 MP4 从 timeline/page/audio 固定快照生成；`internal/api/ExportService` 支持 CreateExport/GetArtifact/CreateDownload（短期签名）；新增 `PlaybackService.GetManifest`，按固定 timeline/page 输入返回内嵌 timeline JSON、页面/音频/字幕签名资源、TTL，资源顺序跟 timeline。`cmd/worker` 已分发 export。媒体+导出+artifact/API 测试覆盖，播放器 UI 待接 |
| G1-8/9 Web 三栏编辑器、S3 适配器 | 🟡 Web 项目入口已接，上传直传待接 | `web/`：Vite + React + TypeScript 独立包；三栏布局（项目/页面 rail / 讲稿 editor / 预览播放器）、debounce 自动保存状态、播放器以单一媒体时钟驱动页面与字幕，不使用独立 `setTimeout`；ProjectService JSON client 已接 `List/Create/Archive`，上传入口已明确提示文件将上传至云端但不伪造未实现直传；Playback manifest 类型与 fetch 客户端已建；响应式移动布局已覆盖；`npm run build` 通过并纳入 CI。S3 适配器已按 G1-9 完成 |

> 说明：G1 后续推进顺序——实现 `UploadService` 授权直传/CompleteUpload，并把 Web 选择文件入口接到真实上传解析任务；正式 TTS 供应商验收受凭据阻塞。带 PG 的测试命令：`PPTS_TEST_DATABASE=... go test -tags=pg -p 1 ./...`。go-pptx 本地产库在 ADR-017 重构中间态触发 `BUG-001` 时，本地 ppts 门禁临时使用 `GOWORK=off`（发布 tag v1.0.1，与 CI 一致），不越界修改依赖仓库。

### 4.3 G2 AI 产品化（2–3周）

| 编号 | 任务 | 验收 |
|---|---|---|
| G2-1 | 双通道理解：结构通道 + 页面截图视觉通道；来源锚点模型（source_slide_id/shape_id/原始值/单位/confidence） | 图文关系基于证据，非 OCR 拼接 |
| G2-2 | 原文朗读 / 润色讲解 / AI 生成讲解三种模式；并排对比与接受修改 | 编辑不覆盖审核稿；锁定稿不被后台覆盖 |
| G2-3 | 数字/单位/型号/日期确定性校验器 + 定向重生成（有限迭代） | 首批 100 页评测集；零未经批准数字变更 |
| G2-4 | 读音词典（用户/租户/项目层级、版本号）、spoken_text/display_text 映射 | 数字缩写读法与字幕一致 |
| G2-5 | 局部重生成（分段级）、时长控制（先缩扩稿后调语速）、预算预占 | 超预算前暂停并给选择 |
| G2-6 | 专业语料 AI 评测集（≥100 页）与人工审核门禁 | 关键数字全部忠于来源 |
| G2-7 | 内容哈希去重：同租户跨 revision/跨项目复用 `content_hash` 相同的分段音频与成品（避免重复 TTS 调用与重复存储，V4.0 §12.5） | 局部重生成只产生增量对象；去重命中率可观测 |

**放行门禁**：专业语料门禁通过；AI 草稿不覆盖用户已审核内容。

### 4.4 G3 商用加固（2–3周）

| 编号 | 任务 | 验收 |
|---|---|---|
| G3-1 | RLS（事务级 set_config、FORCE、运行账号非 owner/BYPASSRLS）、跨租户约束测试 | 上下文缺失拒绝；伪造租户写入失败 |
| G3-2 | 额度预占→执行→结算/释放；用量账本唯一键；供应商成本与用户计费分离 | 并发超额测试、重复扣费测试通过 |
| G3-3 | 身份认证（OIDC Authorization Code + PKCE）、角色（Owner/Admin/Editor/Reviewer/Viewer） | 权限矩阵测试 |
| G3-4 | 审计日志、备份与恢复演练（RPO≤15min / RTO≤2h 目标） | 联合恢复演练 |
| G3-5 | 故障注入：worker 强杀、供应商限流、磁盘满、孤儿资产清理、未知供应商结果对账 | 操作手册覆盖 |
| G3-6 | 存储加固：按租户多后端路由（配置持久化为租户策略，S3/本地/BYOS）+ 生命周期分层（源热存储、音频母版低频、成品按分享有效期、失效产物删除）+ 存储成本按租户汇总 + 企业信封加密可选（V4.0 §12.4/12.5） | 多后端切换无业务改动；生命周期策略生效可验证；跨租户对象签名独立单测通过 |
| G3-7 | 数据保留：源文件默认保留期（独立于派生产物、默认更短）+ 到期清理 + "处理完成后删除源文件"选项执行（V4.0 §13.1） | 到期对象按策略清理；删除后仅保留必要派生产物 |

**放行门禁**：跨租户与重复扣费测试通过；对象签名与生命周期测试通过；备份恢复演练有记录。

### 4.5 G4 统一原生客户端（3–5周）

| 编号 | 任务 | 验收 |
|---|---|---|
| G4-1 | 共享前端包拆分（ui/editor/platform/api-client），Web 构建不加载 Tauri 运行时 | 桌面/移动复用组件、类型、规则 |
| G4-2 | Tauri 桌面：三栏编辑器、本地草稿/云端工程、Go sidecar 全链路（go-pptx/本地工程/任务恢复/媒体编排） | IPC 故障、sidecar 强杀、父进程退出、协议不匹配测试 |
| G4-3 | Tauri 移动：云端上传/任务管理/成品下载/分享；移动单页编辑、审阅、播放布局 | 真机 Android/iOS |
| G4-4 | 移动 L1/L2 离线：manifest 下载与校验、本地缓存播放、离线编辑与待同步记录 | 缓存租户隔离、断网播放 |
| G4-5 | 原生播放适配器（load/play/pause/seek/setRate/getPosition + 事件），平台内唯一权威时钟 | 锁屏朗读、耳机/来电/音频焦点恢复、倍速与断点恢复 |
| G4-6 | 权限与凭据：capabilities 最小命令集、系统安全存储、OIDC 回调 | 权限拒绝、远程网页无本地执行能力 |
| G4-7 | 同步冲突处理（operation_id + expected_revision + 差异展示/分支副本） | 冲突验收通过 |
| G4-8 | 构建与发行：桌面签名（Windows x64 / macOS arm64）+ Updater；移动应用商店/企业分发流程 | 灰度升级、工程迁移、断点恢复 |
| G4-9 | 桌面隐私模式（V4.0 §11.10 / ADR-015）：显式开关 + 开启前本地能力包完整度检测 + 界面逐项"可用/不可用"能力清单；移动端不提供且文案不暗示 | 能力边界逐项可见；无本地能力时提示不可用而非静默降级 |

**放行门禁**：真机音频、IPC 恢复、签名发行、同步冲突验收通过。

### 4.6 G5 能力扩展（独立估算）

桌面 L3 完整离线生成（本地渲染/LibreOffice + 本地 TTS 能力包，支撑隐私模式完整闭环）、认证动画视频、数字人、多语言版本、SSO 高级集成、LMS/SCORM 集成；若"暂停提问"立项，按需引入 pgvector（V4.0 §6.6，不引入独立向量数据库）。每项以独立 ADR + 支持矩阵 + 回归语料作为立项前提。

## 5. 团队角色与资源

| 角色 | 人数 | 投入 | 负责 |
|---|---|---|---|
| Go 后端 | 1 | G0–G5 全程 | 领域、任务系统、AI 适配、导出、RLS/账本；G0–G4 为主 |
| 前端（React/TS） | 1 | G0–G5 全程 | 共享编辑器、播放器、三端 UI；G1/G2/G4 为主 |
| 测试/产品支持 | 兼职 | 全程 | 兼容性语料、AI 评测集、真机矩阵、故障演练 |
| Rust/移动工程支持 | 按需 | G0、G4 | Tauri 壳、sidecar 生命周期、Android/iOS 原生音频与插件 |

关键路径：G0-1/G0-2（go-pptx 与渲染）→ G1-2/G1-3（解析与任务）→ G1-5/G1-7（TTS 与导出）→ G2-1/G2-3（AI 质量）→ G4-4/G4-5（移动离线与原生音频）。存储端口（G0-9）为 G1–G3 存储任务的公共前置。

## 6. 风险与应对

| 风险 | 等级 | 缓解 | 触发重新排期信号 |
|---|---|---|---|
| go-pptx 渲染能力缺失（Render Untested） | 中 | G0-2 LibreOffice 基线先行；不动摇 go-pptx Inspect/Edit 复用 | G0-2 认证语料渲染失败 |
| 带音频 PPTX 配音（Play Partial） | 中 | G0-8 独立门禁；不过则保留实验状态，先交付 Web/MP4 | G0-8 认证客户端自动播音失败 |
| 复杂 PPT 渲染兼容性 | 中 | 固定 LibreOffice/字体/版本；逐页兼容报告；动画按清单降级 | 认证语料损坏/修复弹窗 >0 |
| 字幕对齐精度（专业词/中英混读） | 中 | 供应商时间戳→强制对齐→估算三级降级；独立评测语料 | P95 句首偏差持续超 200ms |
| 移动后台音频/锁屏行为 | 中 | G0 真机原型先行；原生音频会话按系统实现；L1 缓存后才承诺无网播放 | G0-5 真机验证失败 |
| go-pptx 依赖漂移（本地产库 vs 发布 tag） | 低 | 开发期 go.work + 发布锁 tag；CI 用 tag 拉取 | 本地产库破坏 v1.0 兼容契约 |
| 对象存储多后端/生命周期复杂度 | 中 | G0-9/G1-9 先固化端口与本地/S3 适配器；G3 再引入按租户路由/BYOS/生命周期；首版仅 S3+本地 | 多后端配置与业务代码耦合 |
| 隐私模式能力缺口（本地 LLM/TTS 未认证） | 中 | G4-9 能力清单逐项声明；未装能力包显式"不可用"；不承诺本地 AI 草稿（V4.0 §11.10/§16.2） | 用户开启后关键功能不可用引发投诉 |
| 单人开发进度 | 高 | 按里程碑门禁滚动排期；P0 优先 Web/MP4 闭环 | G1 超期则压缩 G2 范围 |
| 供应商限流/成本 | 中 | 并发信号量、预算预占、按段缓存、失败步骤定向重做 | 429/成本超预算持续 |

## 7. 发布与验收节奏

- 每阶段结束跑该阶段门禁（§4 各表），发布试点前补跑 V4.0 §15.3 关键测试：文档回归、幂等与恢复、多租户（含对象签名）、AI 质量、安全与资源、升级兼容。
- 试点对外承诺范围只写"已实测项"；未测平台、动画、本地模型/能力包不标支持。
- 每次里程碑产出 ADR 增量与回归语料，G5 立项必须复用同一套评测门禁（V4.0 §17 ADR-010）。

## 8. 首批两周行动清单

1. 初始化仓库：`git init` + remote `https://github.com/F31/ppts.git`；`go.work` 指向 `E:\projects\go-pptx`；搭 CI、契约与迁移骨架（§3）。
2. 并行：G0-1 go-pptx 适配集成 + 能力对照表、G0-9 `ObjectStore` 端口与本地适配器、G0-4 Tauri sidecar IPC 原型、G0-5 移动音频原型。
3. 确定 TTS 供应商与目标 PowerPoint 认证版本，登记到 ADR。
4. 建首批兼容性语料（≥10 份，复用 go-pptx 语料）与 AI 评测集（≥100 页）登记表。
5. 冻结 P0 页面交互走查，锁定三栏/单页布局与四步流程；上传界面文案明确"文件将上传至云端"（V4.0 §13.1）。

## 9. 变更记录

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-09-12 | V1.0 | 初版：按 V3.6 规划 G0–G5、任务分解、风险与门禁 |
| 2026-09-12 | V1.1 | 基线升级至 V4.0：新增 G0-9、G1-9、G2-7、G3-6/3-7、G4-9；§1.2 暂缓项与 §6 风险按 V4.0 更新 |
| 2026-09-12 | V1.1 | G0 实现登记：仓库骨架/存储端口/文档适配器/渲染/MP4/sidecar 协议/语料库，见 §4.1"G0 实测进度" |
