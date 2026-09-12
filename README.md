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

cd web && npm ci && npm run build
```

当前 Connect API 已提供 Project/Script/Narration/Playback/Export/Upload 主链路：
- `ProjectService`：`Create/Get/List/Archive/GetSlides`；
- `UploadService`：`CreateUpload/CompleteUpload/AbortUpload`（授权直传：分配受限对象键与预签名写链接，完成时校验大小/哈希/租户所有权后才创建源版本并入队解析任务）；
- `ScriptService`：`Get/Update/Approve/Lock/GenerateDraft`（原文讲稿生成）；`NarrationService.CreateGeneration`；`ExportService`；`PlaybackService.GetNarration/GetManifest`（GetNarration 发现最近成功配音时间轴，容器化页面渲染未就绪时 GetManifest 允许无页面图）。

Web 真实链路：直传 → 解析 → 展示真实页面 rail → 生成原文讲稿 → 生成配音 → 拉取真实播放 manifest（音频+字幕）；`local://` 预签名链接由 API 的 `/ppts/object/{key}` 端点服务，S3 后端返回原生预签名 URL。

## 本地服务

API 依赖已应用迁移的 PostgreSQL。G1 开发身份由可信上游头
`X-PPTS-Tenant-ID` / `X-PPTS-User-ID` 注入；G3 将替换为 OIDC 校验。

```bash
PPTS_DATABASE_URL='postgres://...' \
PPTS_OBJECT_ROOT='./var/ppts-objects' \
PPTS_OBJECT_SECRET='dev-download-secret' \
GOWORK=off go run ./cmd/api

PPTS_DATABASE_URL='postgres://...' \
PPTS_TENANT_ID='00000000-0000-0000-0000-000000000000' \
PPTS_OBJECT_ROOT='./var/ppts-objects' \
PPTS_TTS_PROVIDER='fake' \
GOWORK=off go run ./cmd/worker
```

`fake` TTS 只生成开发测试用静音 WAV，不构成 G1-5 正式供应商验收。
