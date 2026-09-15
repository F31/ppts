# PPT 智能语音讲解平台 — 控制台产品优化实施计划 V1.0

> 版本：V1.0｜日期：2026-09-15
> 输入基线：《PPT智能语音讲解平台-控制台设计方案-V1_6.md》（下称 V1.6）
> 代码基线：`ppts` 仓库工作区实际状态（前端 `web/`、后端 `internal/`、契约 `proto/ppts/v1`）
> 审视方式：V1.6 逐章要求 → 代码逐文件核对 → 记录"已实现 / 部分实现 / 缺失"及证据
> 本计划不修改 V4.0 后端架构，不新增 P2 能力为 P0 前置

---

## 0. 一句话结论

**后端主链路完成度高，前端控制台"能跑通闭环"但与 V1.6 基线存在明显落差**，落差集中在三处结构性缺口：

1. **公开区（V1.6 核心增量）整体为 0**——前端无 PublicShell / 公开首页 / 作品展示 / 匿名播放页，后端所有 RPC 一律强制鉴权（`internal/api/server.go:71-97`），且无匿名可读接口。
2. **工作台创作深度不足**——后端已实现的"确认/锁定"未接入 UI，局部重生成后端未实现，播放器**不播放真实音频**（用 `performance.now()` 模拟时钟），页面渲染图未用于缩略图与预览。
3. **产品化与合规细节**——权限边界仅到"显示角色名"、首页统计用分页长度当总数（违反 A04）、视觉基线与 V1.6 §14 相反（深色 vs 浅灰/白/蓝紫）、公开区可发现性（title/OG/预渲染/分离构建）为零。

V1.6 §17 的 29 条验收条款现状：**通过 2 条、部分达成 9 条、未通过 18 条**。

---

## 1. 现状摸底（代码事实）

### 1.1 后端：能力齐备度高

| 维度 | 事实 |
|---|---|
| 服务面 | 8 个 Connect 服务，41 个 RPC（`proto/ppts/v1/`）；Project/Upload/Script/Narration/Playback/Export/Job/Tenant 主链路可用 |
| 已实现且前端未用 | `ScriptService.Approve`/`Lock`（`internal/api/script.go:75,93`）、`ExportService.GetArtifact`（`export.go:81`）、`JobService.WatchEvents`（事件表已建，`migrations/0019_job_events.sql`）、`ProjectService.Get`、`TenantService.Roles/ExportTenant/PurgeTenant` |
| 声明未实现 | `NarrationService.RegenerateSegments` 仅在 `gen/ppts/v1/narration.pb.go` 存在，`internal/api/narration.go` 内嵌 `UnimplementedNarrationServiceHandler`，**无 handler 方法** |
| 缺失能力 | 无匿名可读接口；无产物列表 RPC（`artifact.Store` 仅 `Create/Get`，`internal/artifact/model.go:42`）；无"我的租户列表"接口（切租户无法实现）；无个人偏好存储 |
| 工程完成度 | RLS/审计/额度/保留清理/OTel/幂等/跨租户调度/价格分账均落地，`README.md` 有完整说明 |

### 1.2 前端：控制台骨架已成型

- 技术栈：React + TypeScript + Vite，**手写 `fetch` 调 Connect JSON**（`api.ts`），未使用生成的 Connect 客户端。
- 路由：**History API 路由**（`router.tsx`；原 hash 路由 `#/projects/...` 已在挂载时改写为 `/projects/...`，dev 由 Vite SPA fallback 兜底，prod 由 Go catch-all 经 `PPTS_WEB_ROOT` 启用）。**【已实施，提交 b96352b / e1e6169】**
- Shell：单一 `AppShell`（侧栏品牌+租户短 ID / 主导航 / 顶栏面包屑+语言+用户菜单）。
- 页面：Login、Home、Projects、ProjectEditor、ProjectArtifacts、Jobs、Settings×(Models/Members/Dictionary/Usage/Audit)。
- 组件：ScriptEditor（textarea 整体编辑）、Player（模拟时钟）、ImportDialog（点击选文件）、GatewaySettings、AuditPanel。
- i18n：zh/en 各 451 键，**无 public/showcase 命名空间**。
- 样式：357 行单文件 `styles.css`，**深色主题**（`color-scheme: dark`、底色 `#10131a`、青色主色 `#67e8f9`）。
- 工作区有大量未提交改动（`git status` 25 个文件 M/D），说明控制台 UI 正在演进中。

---

## 2. 设计 → 实现 对照矩阵

图例：✅ 已实现　⚠️ 部分实现／有偏差　❌ 缺失

### 2.1 信息架构与路由（§4）

| 要求 | 现状 | 结论 | 证据 |
|---|---|---|---|
| 公开区/控制台区双 Shell 分离 | 未登录直接进 Login；无公开区 | ❌ | `App.tsx:85-91` |
| 公开区路由 `/`、`/showcase`、`/showcase/:publicId` | 全部缺失 | ❌ | 全仓 grep `showcase/publicId` 零命中 |
| 根路径固定渲染公开首页（不再按会话分流） | 根路径落到 `/home`，未登录显示登录页 | ❌ | `App.tsx:146-152` |
| `/projects/:id/settings` 项目配置 | 缺失 | ❌ | — |
| `/artifacts/:artifactId/play` 固定成品播放器 | 缺失 | ❌ | — |
| `/jobs/:jobId` 独立详情路由 | 用 `?job=` query 实现 | ⚠️ | `Jobs.tsx:23` |
| `/settings/profile`、`/settings/preferences` 个人设置 | 整块缺失，语言切换挤在顶栏 | ❌ | `AppShell.tsx:80-82` |
| `/settings/tenant/*` 租户作用域子路由 | 实为 `/settings/*`（无 tenant 段） | ⚠️ | `App.tsx:133-145` |
| 路由参数化 `?q=&status=&sort=&cursor=` | 无搜索/筛选/排序 | ❌ | `Projects.tsx` |
| 切租户（多租户成员） | 仅显示短 ID，无切换入口、无接口 | ❌ | `AppShell.tsx:43,50-52` |
| 顶栏任务状态入口 | 缺失（仅面包屑+语言+用户） | ❌ | `AppShell.tsx:73-106` |

### 2.2 公开首页与登录（§5）

| 要求 | 现状 | 结论 |
|---|---|---|
| 公开首页六区块（导航/首屏/分类/网格/底部 + 卡片字段） | 全缺 | ❌ |
| 响应式卡片网格（1440/1024/640 断点、16:9 骨架、首屏 8~12、查看更多） | 全缺 | ❌ |
| 两档内容来源（官方精选 P0 / 用户公开 P2） | 全缺 | ❌ |
| 展示位为空时隐藏网格不渲染假卡片 | 全缺 | ❌ |
| 公共播放页 + 底部转化区 + 悬停不自动播放 | 全缺 | ❌ |
| 登录页左右分栏、品牌文案、企业认证入口 | 已实现 | ✅ |
| 开发身份仅显式门控 | `isDevIdentityEnabled() = !oidcConfigured() \|\| DEV`，**生产构建未配 OIDC 时会暴露开发身份表单** | ❌ A01 |
| 正式环境未配认证 → "登录服务尚未配置"且不回退 | 回退到开发身份表单 | ❌ |
| 公开区"注册"按钮（P2 未开放前 → "申请试用"或隐藏） | 无任何注册/试用入口 | ❌ |
| 多租户选择（多项时显示可搜索列表） | 无 | ❌ |
| 401 重新认证 / 403 无权访问（非无限跳登录） | 无全局拦截器，无 403 页 | ⚠️ |
| 首次空状态（一句引导+三步+唯一主按钮） | 有引导与按钮，但按钮跳项目列表（需 3 步才导入） | ⚠️ |
| 服务未配置时管理员/普通用户差异化文案 | 仅首页模型统计卡，无角色区分 | ⚠️ |

### 2.3 首页与项目列表（§6）

| 要求 | 现状 | 结论 |
|---|---|---|
| 欢迎 + 最近项目 4~6 项 | 已实现（6 项） | ✅ |
| 待处理事项（讲稿待确认/音频需更新/失败任务，可跳具体页） | 缺失 | ❌ |
| 紧凑统计（项目总数/可播放/进行中） | 有 6 个统计卡 | ⚠️ |
| 分页 List 长度不得当总数（A04） | `projects.length`（pageSize=20）直接当总数 | ❌ |
| 统计标签区分计数对象 | "可播放项目"实为**最近 6 项**中的数量，口径失真 | ❌ |
| 用量带周期与单位、存储标统计时间 | 有 `statsNote` 但无周期 | ⚠️ |
| 最近完成成品 | 缺失 | ❌ |
| 管理员配置提醒 | 仅模型网关状态一卡 | ⚠️ |
| 项目列表默认卡片视图 + 表格切换 + 记忆偏好 | 仅表格视图 | ❌ |
| 卡片字段（封面/页数/修改时间/处理提示/成品数） | 仅页数、创建时间（非修改时间），无成品数 | ⚠️ |
| 名称搜索 / 状态筛选 / 排序 / 写入 URL | 全缺；后端 `ListProjectsRequest` 亦无 q/status/sort | ❌ |
| 主操作按状态变化（创建稿/审阅/继续编辑/更新配音） | 固定"查看/配音/导入/归档" | ❌ |
| 更多菜单（重命名/查看成品/项目设置/归档） | 仅归档 | ⚠️ |
| "新建讲解"→ 导入弹窗一体化 | 内联标题输入框，导入是行内另一个按钮 | ⚠️ |

### 2.4 导入与解析（§7）

| 要求 | 现状 | 结论 |
|---|---|---|
| 拖放 + 选文件 | 仅点击选文件 | ❌ |
| 显示服务端真实格式/大小/页数限制 | 缺失 | ❌ |
| 上传真实百分比 | 仅阶段文案，无进度 | ❌ |
| 取消/续传 | `abortUpload` 有 API 但未暴露取消按钮 | ❌ |
| 解析报告按页（字体替换/嵌入媒体/动画限制/隐藏页） | 缺失 | ❌ |
| 页面陆续可用（已处理页数/缩略图） | 缺失 | ❌ |
| 讲解设置非阻塞面板（模式/受众/风格/语言/目标时长/音色） | 缺失 | ❌ |
| "查看任务"入口 | 仅把 jobId 拼进文案 | ⚠️ |
| CompleteUpload 前不宣称完成 | 流程正确 | ✅ |

### 2.5 创作工作台（§8）——缺口最集中

| 要求 | 现状 | 结论 |
|---|---|---|
| 三栏（缩略图 + **PPT 预览** + 讲稿）+ 属性在页签/抽屉 | 三栏（缩略图列表 + 讲稿 + 属性），**无 PPT 页面预览** | ⚠️ |
| SlideNavigator 缩略图 | 只有序号 + 标题文字；后端已产出 `render/page-NNNN.png` 未使用 | ❌ |
| SlidePreview（渲染图/缩放/来源定位） | 缺失（属性栏是文字预览） | ❌ |
| ScriptPanel 分段讲稿 | 单 textarea 整体编辑，靠 `\n\n` 切分 | ⚠️ |
| PropertiesPanel 音色/语速/目标时长 | 有音色下拉与语速下拉，无目标时长 | ⚠️ |
| PlaybackBar 固定底部、进度来自实际媒体时钟 | 播放器在页面下方区块；**无 `<audio>`，用 `performance.now()` 模拟** | ❌ A21 |
| 讲稿三模式 | 已实现 | ✅ |
| 原文朗读不自动改写、display/spoken 分离 | 已实现（两字段分离） | ✅ |
| 无备注页显式选择来源 | 缺失 | ❌ A08 |
| AI 操作范围显示 | 只有全篇生成 | ❌ |
| 段落工具栏（缩短/润色/衔接/发音/停顿） | 缺失 | ❌ |
| 来源锚点展示 | 有 anchor-strip | ✅ |
| 来源可点击定位到页对象 | 缺失（展示裸文本） | ⚠️ |
| 读音调整（本处/本项目/租户词典 + 影响范围回执） | 只有设置页整本词典 CRUD | ❌ A13 |
| 音色选择器（语言/风格筛选、样例试听、默认 vs 单页覆盖） | 原始 voice 字符串下拉 | ❌ |
| 三类按钮区分（试听样例/本页试听/播放已有） | 全缺；无"本页试听" | ❌ |
| **确认 / 锁定 UI** | **后端已实现 Approve/Lock，前端 API 层未封装、UI 无入口** | ❌ A09 |
| 保存 800ms 防抖 + 五态显示 | 500+220ms 防抖；状态仅 saved/dirty/saving，**无 error/conflict 态** | ⚠️ |
| 中文输入法组合期不提交 | 无 composition 处理 | ❌ A10 |
| 页面切换刷新待保存队列 / Ctrl+S / 离开提示 | 全缺 | ❌ |
| 冲突对照 + 保留本地稿 + 不最后写入覆盖 | 有简单对照弹窗 | ✅ A12 |
| 未决冲突不允许生成最新稿 | 无阻断 | ❌ |
| 生成面板（范围/待确认稿数/需新生成/预计时长/用量） | 只有 estimate 时长 | ⚠️ |
| 正式生成以已确认为输入、拒绝未确认稿 | 只要"讲稿都存在"即可生成，不校验 confirmed/locked | ❌ |
| **语速/仅锁定参与生效** | UI 有语速下拉，但 `createGeneration` **未传 `rate_percent`、`lock_confirmed_only`**（proto 已定义） | ❌ 真缺陷 |
| 生成中继续编辑 + 顶部快照提示 | 缺失 | ❌ A15 |
| 重复点击/幂等 | 有 idempotencyKey | ✅ A16 |

### 2.6 状态模型与增量更新（§9）

| 要求 | 现状 | 结论 |
|---|---|---|
| 讲稿 empty/draft/pending/approved + locked 独立 | 类型与后端已支持 | ✅ |
| 配音 missing/queued/running/ready/**stale**/failed | 前端只有 `ready` 布尔；"需更新"未实现 | ❌ |
| 修改单页 → "音频需更新" + 旧音频标"此前版本" | 缺失 | ❌ A14 |
| 改全篇音色 → 显示受影响页数 | 缺失 | ❌ |
| 改字幕样式 → 提示重新导出 | 缺失 | ❌ |
| 调整页序 → 衔接待复核 | 缺失（无排序 UI） | ❌ |
| 局部重生成 | 前端无 UI，后端 `RegenerateSegments` 未实现 | ❌ |
| 状态映射集中维护、组件不猜枚举 | `types.ts` 有 jobState/role 映射；配音/能力状态未集中 | ⚠️ |

### 2.7 任务中心与实时反馈（§10）

| 要求 | 现状 | 结论 |
|---|---|---|
| 列表 + 详情 + 取消 + 重试 | 已实现 | ✅ |
| 轮询（活跃 2~3s / 后台 10~15s） | 统一 5s | ⚠️ |
| WatchEvents 服务端流 + 断线回退 | 后端已实现，前端未接 | ❌ |
| 列表列：项目/类型/提交时间/**范围**/**阶段**/进度/状态 | 缺"范围""阶段"，项目只显示 id 前 12 位 | ⚠️ |
| 详情：步骤(JobStep)/受影响页/输入版本/traceId | 缺步骤、受影响页、traceId | ❌ |
| "正在取消" → 服务端确认后"已取消" | 有枚举与样式，无过渡文案 | ⚠️ |
| 局部失败"重试失败部分" | RetryFailed 为任务级 | ⚠️ A18 |
| 供应商结果未知"正在核实生成结果" | 有 `UNKNOWN_PROVIDER_RESULT` 枚举与文案 | ⚠️ |
| 桌面通知（可选） | 缺失 | ❌ |

### 2.8 成品、播放器与导出（§11）

| 要求 | 现状 | 结论 |
|---|---|---|
| 项目"成品与版本"按快照分组列产物 | **无产物列表**，只有快照摘要 + 一句占位说明 | ❌ |
| 产物下载（CreateDownload） | API 未接 | ❌ |
| 成品 ↔ 项目双向跳转 | 有返回编辑器 | ⚠️ |
| 播放器音频真实播放 | **无音频元素，模拟时钟** | ❌ A21 |
| 播放/暂停/拖动/上下一页/目录 | 播放暂停✔ 拖动✔ 页目录为 slide-map | ⚠️ |
| 倍速 / 全屏 / 缓冲状态 | 全缺 | ❌ |
| 固定成品播放器路由 | 缺失 | ❌ |
| 导出类型选择（MP4/音频/字幕/配音 PPTX） | 仅 `WEB_PROJECT` 一种，无选择对话框 | ❌ |
| 导出绑定快照、不混新字幕旧音频 | 用 manifest 的 timelineKey/pagePngKeys，方向正确 | ⚠️ |
| 发布为公开作品（P2） | 全缺 | ❌ A29 |

### 2.9 设置、权限与多端（§12–13）

| 要求 | 现状 | 结论 |
|---|---|---|
| 租户设置 5 项（模型/成员/词典/用量/审计） | 全部实现 | ✅ |
| 个人设置（资料/语言/密度/通知偏好） | 缺失 | ❌ |
| 权限矩阵落到按钮/路由（PermissionBoundary） | 仅读 role 显示名称，无按钮级/路由级边界 | ❌ A22 |
| 模型服务"已配置/检测通过/当前不可用"区分 | GatewaySettings 有 test 能力，展示需核对细化 | ⚠️ |
| 模拟供应商"演示模式"标识 | 部分（fake TTS 有说明），UI 未显式 | ⚠️ |
| Web 响应式断点 | CSS 有 1280/1040/860 断点 | ✅ |
| 桌面 Tauri / 移动 Tauri | `web/apps/native` 目录存在，需核对成熟度 | ⚠️ |
| 处理位置可见性 / 离线语义 | 缺失 | ❌ A24 |

### 2.10 视觉规范、工程组织与验收（§14–17）

| 要求 | 现状 | 结论 |
|---|---|---|
| 浅灰工作区 + 白色面板 + 单一蓝紫主色 | **深色主题**（`#10131a` + 青 `#67e8f9`），与基线相反 | ❌ |
| 圆角 6~8px 控件 / 10~12px 卡片 | 大量 999px 胶囊、16~24px 卡片 | ❌ |
| 字号/间距/密度基线 | 未系统化，无 token 体系 | ⚠️ |
| 状态=图标+文字+颜色 | 主要为文字+颜色 | ⚠️ |
| 键盘：可见焦点/弹窗焦点管理/Esc/焦点返回 | 缺 focus trap 与 Esc | ❌ |
| 播放快捷键（空格/方向键不抢占输入） | 缺失 | ❌ A25 |
| 对比度 ≥4.5:1、减少动画、播报 | 未验证 | ⚠️ |
| 触控目标 ≈44px | 未落实 | ❌ |
| 使用生成的 Connect 客户端 | 手写 fetch + JSON，已出现字段漏传（rate_percent） | ⚠️ |
| 公开区/控制台分离构建 | 单 bundle | ❌ |
| 公开页 SEO/预渲染 + title/OG | `index.html` 标题仍为 `PPTS Web`，无 description/OG，无预渲染 | ❌ A27 |
| API proxy 前置核对 | `vite.config.ts` 已配 4 条代理 | ✅ |
| A01–A29 验收 | 通过 2 / 部分 9 / 未通过 18 | ❌ |

---

## 3. 缺口清单（按阻塞性与价值分级）

### 3.1 P0 阻断项（不完成无法通过 P0 闭环验收）

| ID | 缺口 | 影响条款 |
|---|---|---|
| G-01 | 公开区整套缺失（Shell/首页/展示/匿名播放页） | A27、A28 |
| G-02 | 后端无匿名可读接口（公开列表/详情/资源） | A27 |
| G-03 | 播放器无真实音频播放（模拟时钟） | A21、A14 |
| G-04 | 确认/锁定未接入 UI（后端已就绪） | A09 |
| G-05 | 生成入参漏传 `rate_percent` / `lock_confirmed_only`；不校验稿件确认态即生成 | A09、A15 |
| G-06 | 项目列表无搜索/筛选/排序；后端 `ListProjects` 无对应字段 | §6.2 |
| G-07 | 无产物列表与下载（`GetArtifact` 未接、无 List RPC） | A20 |
| G-08 | 前端无权限边界（按钮/路由级） | A22 |
| G-09 | 首页统计口径违规（分页长度当总数） | A04 |
| G-10 | 开发身份门控不满足 A01（未配 OIDC 即暴露） | A01 |

### 3.2 P0 高价值快速修复（后端已就绪，成本低收益高）

| ID | 内容 | 依据 |
|---|---|---|
| Q-01 | 封装并接入 `ScriptService.Approve` / `Lock` | `script.go:75,93` |
| Q-02 | 接入 `ExportService.GetArtifact` + `CreateDownload`，产出下载入口 | `export.go:81,93` |
| Q-03 | 接入 `JobService.WatchEvents` 流，保留轮询回退 | `job.proto:16` |
| Q-04 | 播放器接入 `PLAYBACK_RESOURCE_TYPE_AUDIO`，以媒体时钟驱动 | `types.ts:36`、`playback.proto` |
| Q-05 | 缩略图/预览改用后端已产出的 `render/page-NNNN.png` | `README.md:48` |
| Q-06 | 修正 `CreateGeneration` / `Estimate` 入参（rate/mode/`lock_confirmed_only`） | `narration.proto` |

### 3.3 P1 完整产品化

首页待处理事项、项目卡片视图与偏好记忆、导入拖放与真实进度、解析报告、讲解设置面板、段落工具栏、读音调整、音色选择器、增量更新（需后端补 `RegenerateSegments`）、配音 stale 状态、任务阶段/步骤/traceId、个人设置页、模型健康三态、移动端布局收敛。

### 3.4 P2 可选增强

全局成品库、命令面板、邮箱自助注册、**用户作品公开发布与撤回（含 publicId、级联失效、CDN 清理）**、桌面/移动原生、离线与隐私模式、复杂时间轴、多人协作。

---

## 4. D0 批次：先行缺陷修复（建议 1 个迭代内完成）

这些是"设计已明确、后端已就绪或成本极低"的确定性修复，不依赖任何未定决策，建议在 B1 之前或并行完成。

| 编号 | 缺陷 | 位置 | 修复动作 | 验收 |
|---|---|---|---|---|
| D0-1 | 语速/仅锁定参与未生效 | `api.ts:175-188` | `createGeneration`/`estimateNarration` 补 `ratePercent`、`lockConfirmedOnly`、`mode` 入参，编辑器透传 | 生成请求含真实字段；A09 |
| D0-2 | 开发身份门控 | `auth.ts:24-27` | 改为"仅 `import.meta.env.DEV` 显式开放"，生产构建未配 OIDC 时渲染"登录服务尚未配置"文案 | A01、A02 |
| D0-3 | 首页项目总数口径 | `Home.tsx:108` | 改用真实聚合能力或标"暂无统计"；统计卡标签写明计数对象 | A04 |
| D0-4 | 可播放项目统计失真 | `Home.tsx:69-81,109` | 明确"最近 N 项中可播放数"或改由聚合接口给出 | A04 |
| D0-5 | 播放器无音频 | `Player.tsx:19-36` | 引入 `<audio>` 播放 manifest 中 AUDIO 资源，进度以 `timeupdate` 为准；保留无音频降级 | A21 |
| D0-6 | 保存态缺 error/conflict | `ScriptEditor.tsx:15,67` | 五态显示；失败/冲突保留输入并可重试 | A11、A12 |
| D0-7 | 中文输入法组合期提交 | `ScriptEditor.tsx:69-76` | 处理 `compositionstart/end`，组合期不触发保存 | A10 |
| D0-8 | 公共可发现性基线 | `web/index.html` | 标题改"智讲 PPT"、补 `description`/OG 标签、`lang` 与 favicon | A27 |
| D0-9 | 死代码与样式残留 | `styles.css`（`.workspace`/`.project-panel`/`.slide-rail`） | 清理已删除组件 `ProjectPanel.tsx` 的残留样式 | 工程质量 |

---

## 5. 实施批次计划（B0–B5）

> 批次划分沿用 V1.6 §16，按"依赖顺序 + 可独立验收"重排；每批次给出后端/前端/联调分工与出口条件。

### B0 联调基线（先做，不做完不开 B1）

| 项 | 内容 |
|---|---|
| 目标 | 把"设计假设"换成"经核对的真实契约"，消除后续返工 |
| 任务 | ① 逐 RPC 导出真实契约清单（字段/枚举/分页/错误码），形成《接口差异表》；② 核对 Vite 代理与认证透传，确认不会把 SPA HTML 回退解析为 JSON；③ 建立"能力表"（`available/unavailable/unknown`）与统一状态映射模块，取代组件内散落判断；④ 起本地真实环境（API + worker + PG + 对象存储 + fake/beta TTS）跑通登录→列项目→读一项目→读一任务 |
| 交付物 | 接口差异表、能力表、状态映射模块、可复现的本地联调脚本 |
| 出口 | 可登录、列项目、读取一个项目及其任务；差异表逐项标注"已可用/字段适配/需补接口/暂不开放" |
| 备注 | 本计划 §2 已是对照矩阵的第一版，B0 需补字段级细节与真实调用验证 |

### B1 框架与导入（含公开区 P0 形态）

| 项 | 内容 |
|---|---|
| 目标 | 双 Shell 落地；未登录可访问公开首页（官方精选形态）；登录后可导入并看到解析结果 |
| 前端 | ① 抽出 `PublicShell`/`AppShell`/`EditorShell`，公开区组件（`PublicHeader`/`HeroSection`/`ShowcaseGrid`/`ShowcaseCard`/`CategoryFilter`/`PublicPlayer`/`ConversionBar`）不得依赖认证态与租户上下文；② 路由改为 history 路由 + 服务端 fallback，保留 hash 兼容重定向；③ 新增 `/`、`/showcase`、`/showcase/:publicId`、`/projects/:id/settings`、`/settings/profile`、`/settings/preferences`、`/settings/tenant/*`；④ 登录页补"申请试用/隐藏注册"二选一；⑤ 首页空态主按钮直达导入；⑥ 项目列表加卡片视图 + 表格切换 + 偏好记忆；⑦ 导入弹窗补拖放、真实百分比、取消、格式/大小限制、解析报告、"查看任务" |
| 后端 | ① **新增匿名可读服务**（公开列表：分类/排序/分页；公开详情：缩略图/标题/分类/时长/时间轴/音频字幕地址），在 `server.go` 中走**不经 `auth()` 的独立路由**，只返回最小公开字段集；② `ListProjectsRequest` 增加 `q/status/sort` 或新增查询 RPC；③ `Project` 增加 `updated_at`/`slide_count`/`artifact_count`（或等价聚合） |
| 联调 | 确认匿名请求不携带租户上下文；确认公开接口不泄露租户内部字段；确认深链接登录后回到原目标 |
| 出口 | 刷新深链接可恢复；权限不越界；**匿名访问不触发任何需认证请求**；真实导入产生解析报告 |
| 验收 | A01、A03、A05、A07、A27、A28 |

> **路由改造已提前完成（独立于 B1 其余项）**：`router.tsx` 由 hash 改为 History API，`main.tsx` 挂载前执行 `migrateLegacyHash()` 将遗留 `#/path` 改写为 `/path`；`server.go` 新增可选 SPA 兜底（设 `PPTS_WEB_ROOT` 时注册 `GET /{path...}` 返回 `index.html`，并放行 `/ppts/object`、`/healthz`、`/debug` 前缀）。提交 `b96352b`（前端）、`e1e6169`（后端）。
>
> **公开区双 Tab（C-1）已实现**：后端 `publications` 表 + RLS 双策略（`public_read` 仅放行 `status='approved'` 匿名跨租户只读 / `tenant_write` 租户内写），状态机 `draft→pending→approved/rejected`，精选发布 `POST /public/featured`（admin）；匿名只读 `GET /public/works`、`GET /public/works/{id}`（封面走 SignedURL）；受保护只读 `GET /public/works/mine`、`GET /public/works/queue`（admin 审核队列）。前端：浏览链路 `PublicShell` + 作品广场 `Explore`（featured/user 双 Tab）+ 匿名播放页 `Watch`（音频待 B3）+ 未登录放行 `/explore`、`/watch/:id`；控制台管理 `PublicAdmin`（我的发布 / 审核队列，admin 可见审核）+ 项目编辑器「发布到公开区」弹窗（`ProjectEditor`）。各 handler 分别提交于 `63ede93`、`cc506c7`（后端）、`279d820`（浏览前端）、`418d4e7`（管理前端）；**注意**：此前 `server.go` 漏挂 `registerPublicRoutes` 调用且缺 `pgxpool`/`internal/public` import，公开路由实际未生效、且 `go build` 报错，已于 B3 本轮补全。
>
> **B1 导入增强（第⑦项）已实现**：`ImportDialog` 升级为拖拽上传 + 真实上传百分比进度条（`uploadToURL` 改 XHR 支持 `onProgress`）+ 上传中可取消（`AbortController` + `abortUpload`）+ 前端格式（.pptx）/大小（100MB）校验 + 结构化解析报告（warnings 列表）+「查看任务」跳转 `/jobs`；补充 i18n 与样式（提交本轮）。**注**：大小限制为前端校验，后端 `CreateUpload` 是否另强未核（沙箱无法编译）；公开区音频播放（B3）仍待做。
>
> **验证限制**：本环境无法跑 `go build`/`tsc`（依赖未缓存 / 缺原生二进制），改动经 `gofmt` 与逐文件类型复核，最终需本地 `go build ./...` + `npm run build` 确认。

**生产 SPA history 兜底（nginx 参考，前端由 nginx/CDN 托管时）**：
```nginx
# 前端静态资源与 SPA 路由兜底
location / {
  try_files $uri $uri/ /index.html;
}
# 以下前缀仍代理到 Go 后端（与 catch-all 放行的前缀一致）
location /ppts.v1. { proxy_pass http://127.0.0.1:8080; }
location /ppts/object { proxy_pass http://127.0.0.1:8080; }
location /api { proxy_pass http://127.0.0.1:8080; }
location /healthz { proxy_pass http://127.0.0.1:8080; }
```
> 注：若用 Go 单二进制托管前端，设 `PPTS_WEB_ROOT=/path/to/web/dist` 即可，无需 nginx 上述 `location /` 兜底。

**决策依赖**：C-1（公开区双 Tab）、C-2（SEO 元数据 + CSR）、C-3（深色默认 + 浅色切换）、C-4（history + 服务端 fallback）**均已拍板并实施**（见上方 callout 与各 C-* 行状态）。剩余未定项见 §6（C-5 生成口径、C-6 切租户、C-7 个人设置）。分离构建按 C-2 维持「暂不做」。

### B2 核心创作

| 项 | 内容 |
|---|---|
| 目标 | 一份真实 PPT 的连续编辑闭环成立，且不覆盖已确认内容 |
| 前端 | ① 三栏调整为"缩略图（真实渲染图）+ PPT 预览 + 讲稿"，属性改为页签/抽屉；② 讲稿改分段编辑，加段落工具栏（缩短/润色/衔接/发音/停顿）；③ 接入 **Approve/Lock** 按钮与状态；④ 音色选择器（属性筛选 + 样例试听 + 项目默认/单页覆盖）+ 三类按钮严格区分；⑤ 读音调整 popover（本处/本项目/租户词典）并展示受影响段回执；⑥ 无备注页显式选择讲稿来源；⑦ 生成面板补范围/待确认稿数/需新生成/用量，未确认稿默认阻止正式生成；⑧ Ctrl/Cmd+S、离开页面前未保存提示、页面切换刷新待保存队列 |
| 后端 | ① `NarrationService.RegenerateSegments` 落地（分段级重生成已具备 `SegmentIDs` 局部重算能力，`README.md:100`）；② 确认/锁定后使配音进入 **stale** 语义（当前输入 vs 音频关联修订比较）；③ 讲稿接口补"待确认稿"聚合（供生成前置检查） |
| 出口 | 不覆盖已确认稿；保存失败不丢输入；局部重生成只重做实际范围并显式声明 |
| 验收 | A08、A09、A10、A11、A13、A14 |

**执行计划（里程碑门控 + 验证回路）**
> 约束：本环境 `buf`/`protoc` 不可用、`gen/` 为预生成代码不可重生成 → 任何新后端能力若需新增 Connect RPC 方法，须改为**原生 HTTP 端点**或**复用既有 RPC 字段**（优先确认 `GenerateNarrationRequest` 是否已含 `segment_ids` 字段）。沙箱无法 `go build`/`tsc`，每个里程碑交付后需在本地 `go build ./...` + `npm run build` 最终验证。
> 决策已定：C-5 强制阻止未确认稿；C-6 本轮不做切租户（读音调整 popover 的"租户词典"收窄为当前租户）；C-7 明确不做个人设置并移除入口。

- **M1 后端基础（R-6 首位）【已实施】**
  - 分段重生成：proto 已有 `RegenerateSegments` RPC（`RegenerateSegmentsRequest` 含 `segment_ids`），故直接实现 `NarrationGenerationService.RegenerateSegments` handler，复用 `CreateGeneration` 的入队/并发限流/配额预占流程（无需新增 RPC）。
  - 待确认稿聚合：新增 `narration.Store.CountDraftSegments` + 原生 HTTP `GET /projects/{pid}/narration/draft-count`（返回 `{draftSegments}`），供生成面板 C-5 前置检查。
  - stale 语义：新增 `narration_scripts.audio_revision` 列（迁移 0009）+ `MarkAudioRevision`（配音任务完成回写）+ `ListByProject`；原生 HTTP `GET /projects/{pid}/narration/stale` 返回每页 `audioRevision < revision` 的过期信号。
  - **关键修复**：server.go 此前漏挂 `registerPublicRoutes`（仅加了 import 未调用），导致公开区/B3 音频端点从未生效且 `go build` 报 import 未使用；本轮补上挂载（同时挂载 `registerNarrationRoutes`）。
  - 门控：`gofmt` 通过 + 逐文件类型复核 + 测试 fake 同步实现接口新增方法；最终需本地 `go build ./...` + `go test ./...` 确认（沙箱无法编译）。
  - **重大修复（M2 前置）**：提交 `d1f49ba` 修复 `internal/api` 自 B3 起完全无法编译的两处错误——① pronunciation.go 与 public.go 重复声明 `writeJSON`（redeclared）；② `loadPageManifest`/`loadBundle`/`statTenantObject` 被当作包级函数调用却只定义为 `*PlaybackService` 方法。改为包级函数（新增 `objects` 参数）并修正 4 处调用点。此后所有 B3/M1/网关提交在本机均无法 `go build`，需本地重新编译验证。
- **M2 编辑器骨架（① + ⑧）【已实施】**：编辑器重构为三栏（缩略图列表[真实渲染 PNG] + PPT 预览 + 讲稿），属性面板改为右上角按钮唤出的抽屉（①）；新增后端原生端点 `GET /projects/{pid}/slides/render`（复用解析 pages 清单 + 对象存储签名）产出每页渲染图短期可读 URL，缩略图与预览据此展示，缺失时降级为序号/标题（② 真实渲染图落地）。讲稿编辑接入 Ctrl/Cmd+S 立即保存、离开页面前未保存提示、切换页面前先 flush 当前页待保存队列（⑧）；ScriptEditor 改为 forwardRef 暴露 flush/isDirty 并上报五态（saved/dirty/saving/error/conflict），同时补齐中文输入法组合期不误存（A10 顺带）。
  - 配套：api.ts 增 `getJSON` GET 辅助 + `getSlideRenderURLs`；i18n 增 editor.properties/unsaved/slidePreview/renderPending、script.saveError/conflict；styles.css 三栏布局 + 缩略图 + 预览 + 抽屉 + 未保存标记，收敛响应式断点。
- **M3 讲稿编辑增强（② + ③ + ⑥）【已实施】**：
  - ② 分段编辑：ScriptEditor 改为按 segment 渲染独立可编辑卡片（各自 textarea + 状态徽标 + 复选选择），保留 Ctrl+S/flush/IME/五态；段落工具栏 缩短/润色/衔接 → `RegenerateSegments`（M1 已落地，局部重生成，轮询 revision 刷新），发音/停顿 → 在 spokenText 插入朗读标记（正式发音词典为 M4 ⑤）。
  - ③ Approve/Lock：前端封装 `approveScript`/`lockScript`（复用既有 ScriptService RPC），编辑器在 `canReview`（role≠viewer）时显示确认/锁定按钮；锁定后编辑只读、锁定按钮禁用（后端不支持解锁）。
  - ⑥ 无备注页来源：生成草稿面板对 `hasNotes===false` 的页显式展示来源选择（版式正文/仅标题/仅正文/仅备注/自定义），经原生端点 `PUT /projects/{pid}/slides/{sid}/source` 持久化（迁移 0025 + `slide_script_sources` 表 + RLS）；`GET /projects/{pid}/slides/sources` 回读；GenerateDraft handler 注入已存来源到 `ScriptDraftSnapshot.Sources/CustomSources`，`script_draft` worker 在 `pgText`/`pgAnchors` 中尊重该来源。
  - 配套：api.ts 增 approveScript/lockScript/regenerateSegments/setSlideScriptSource/getSlideScriptSources + putJSON；i18n 增 editor.toolbar/shorten/polish/transition/pronounce/pause/approve/lock/source.* 等；styles.css 段落工具栏/分段卡片/来源选择/状态徽标。
- **M4 生成与语音（④ + ⑤ + ⑦）【已实施】**：
  - ⑦ 生成面板增强：属性抽屉新增范围选择（全部已生成页 / 仅当前页）、待确认稿数（拉 M1 端点 `GET /projects/{pid}/narration/draft-count` 的 `draftSegments`）、需新生成数（前端按范围+已生成稿估算）、用量（`estimateNarration` 的 `costMin/costMax` + 时长）；**C-5 强制阻止**：`draftSegments>0` 时正式生成按钮禁用并提示，且 `generateNarration` 传 `lockConfirmedOnly=true`（后端 `CreateGeneration` 的 `RequireConfirmed` 校验，未确认稿返回 `FailedPrecondition`）。**D0-1 真缺陷修复**：`createGeneration`/`estimateNarration` 此前漏传 `rate_percent`/`lock_confirmed_only`，本次补全（后端早就读这两字段，前端未传导致语速/仅确认参与从未生效）。
  - ④ 音色选择器（降级务实）：属性抽屉 voice 下拉升级为音色卡片选择器（点击选中设为项目默认）；**样例试听按钮因后端无 voice catalog（无样例 URL/语言/风格元数据）而禁用并 tooltip 说明**；单页覆盖（per-slide voice）后端 `ScriptRevision` 无 schema 支持，降级为段落工具栏"读音"标记（最轻量 per-segment 覆盖）；语言/风格筛选因无元数据未显示。
  - ⑤ 读音调整 popover：段落工具栏"读音"按钮升级为弹窗——原词(pattern, 预填当前段文本) + 读音(replacement) + 「本处插入」（`〔读：读音〕` 标记写入 spokenText）+ 「添加到租户词典」（调 `POST /api/pronunciation`，复用既有 tenant 级 CRUD，C-6 收窄为当前租户）+ 影响范围回执（实时估算本项目含该词的段数）。
  - 纯前端里程碑：C-5 后端校验、draft-count 端点、发音词典 CRUD 均早已具备，无后端改动。
- **C-7 收尾**：检查设置菜单，隐藏/移除个人设置入口（若已存在）。

> 顺序 M1→M2→M3→M4，每里程碑一个 commit；任一里程碑本地验证失败则回退该里程碑不合并。

### B3 生成与交付

| 项 | 内容 |
|---|---|
| 目标 | 生成 → 播放 → 导出 → 成品可用，形成完整交付 |
| 前端 | ① 任务实时反馈接 `WatchEvents`，断线回退轮询、按 seq 续接；② 播放器补倍速/全屏/缓冲状态/恢复播放不重置第一页；③ 项目"成品与版本"按快照分组列产物（格式/时间/时长/可用性）+ 下载（`CreateDownload`，链接过期可续期）；④ 导出对话框（MP4 分辨率与字幕烧录、音频包、SRT/VTT、配音 PPTX 门禁说明）；⑤ 生成中继续编辑的顶部快照提示 |
| 后端 | ① 新增产物列表 RPC（`artifact.Store` 补 `ListByProject`）；② 确认导出快照完整性与"新字幕不配旧音频"约束；③ `GetArtifact` 前端接入 |
| 出口 | 可重进恢复；旧成品与新稿区分清晰；下载链接可续期 |
| 验收 | A15、A16、A17、A18、A19、A20、A21 |

**执行计划（里程碑门控 + 验证回路）**
> 约束同 B2：本环境 buf/protoc 不可用 → 新后端能力走原生 HTTP 端点；沙箱无法 go build/tsc，每个里程碑交付后需本地 go build ./... + npm run build 验证。
> 决策已定：成品按 snapshotHash 分组展示（artifact 模型无 duration 字段，时长暂不展示，补列列入后续迁移）；下载复用既有 CreateDownload（前端 api.ts 已具备）。

- **B3-M1 成品与版本列表 + 下载【已实施】**：
  - 后端：`artifact.Store` 补 `ListByProject`（postgres.go，tenant.Run 前缀 + 倒序）；新增接口方法；原生 HTTP `GET /projects/{pid}/artifacts`（internal/api/artifact.go，复用 requirePrincipal/requireRole/writeJSON 与 narration 路由样板），server.go 挂载 `registerArtifactRoutes`；返回 `{artifacts:[{id,snapshotHash,format,sizeBytes,createdAt,downloadable}]}`。
  - 前端：api.ts 增 `getProjectArtifacts` + `ProjectArtifact` 类型；ProjectArtifacts.tsx 重写为按 snapshotHash 分组列出成品（格式徽标/导出时间/大小/下载按钮，调 createDownload 取限时直链并触发下载）；i18n 中英文各 13 键（含格式标签与下载态）；styles.css 列表/分组/徽标/下载样式。
  - 验收：A20。注：模型缺 duration 字段，时长列未展示（需迁移补列，列入后续）。

- **B3-M2 播放器真实音频 + 倍速/全屏/缓冲（合并 D0-5）【已实施】**：
  - 前端 `Player.tsx` 重写为接入 `<audio>`：从 `manifest.resources` 抽取 `AUDIO` 资源，与 timeline 段按 `(slideId#segmentId)` / `audioKey` 关联、按时间排序；按段顺序拼接播放，进度以 `audio.currentTime` 为准（替代原合成 rAF 时钟）；`ended` 自动续播下一段；`seek` 经 `seekingRef` 同步音频偏移；无音频时回退原 rAF 墙钟（保留画面+字幕）。
  - 新增倍速（0.5/1/1.25/1.5/2，绑定 `audio.playbackRate`）、全屏（`requestFullscreen` + `fullscreenchange` 同步）、缓冲指示（`waiting`→`canplay/playing`）；恢复播放不重置第一页（`play` 不清零 `positionUs`）。
  - i18n 中英文各 5 键（speed/fullscreen/exitFullscreen/buffering/noAudio）；`styles.css` + `theme.css`(light) 缓冲/倍速/全屏/提示样式；切换 `manifest` 时复位播放状态避免串音。
  - 验收：A21。注：后端 `playback` 已在 B3 地基补全 `AUDIO` 资源（`signManifestResources`），本里程碑纯前端；本机需 `go build ./...` + `npm run build` 终验。

- **B3-M3 任务实时反馈 WatchEvents【已实施】**：
  - 后端：`WatchEvents`（internal/api/job.go:142）已就绪，`pipeline.PGStore.EventsSince`（postgres.go:199）已实现单调 seq 事件表，流可用（非 Unimplemented）。本里程碑无需后端改动。
  - 前端：`api.ts` 新增 `watchJobEvents`（解析 Connect 协议 streaming 信封：1 字节 flag + 4 字节大端长度 + JSON 消息）；`Jobs.tsx` 按项目维度开流、逐条合并更新（按 jobId 定位），任一项目流失败则按 seq 续接重连（最多 3 次、指数退避），仍失败彻底回退到既有 5s 轮询（断线回退轮询）；无活跃任务时不持有流。顶部"实时推送"绿点指示连接状态；任务详情补 `lastError.traceId`（proto 已带）。
  - 验收：A16、A17、A18、A19。注：范围/阶段/步骤/受影响页/traceId 列属 B4-⑥（见 §5 B4 前端⑥），不在本里程碑；流式仅推送 Job 基础字段。本机需 `npm run build` 终验（沙箱无法 tsc）。

- **B3-M4 导出对话框 + 快照完整性【已实施】**：
  - 后端（快照完整性修复）：`app.ExportSnapshot` 补 `BurnSubtitles`/`IncludeNotes` 字段；`CreateExport` 从请求（proto 已有 `burn_subtitles`/`include_notes`）写入快照。此前两选项被接收却未入快照，导致仅选项不同的导出得到相同 snapshotHash（快照不完整）。
  - 前端（修复 D0 真缺陷）：`api.ts` `createExport` 此前**从不发送 `Idempotency-Key`**（后端强制要求，export.go:62-65），前端导出必然 InvalidArgument；现补 `idempotencyKey` 参数与请求头（沿用 `export-{projectId}-{ts}` 约定）。
  - 前端（导出对话框）：新增 `components/ExportDialog.tsx`——类型选择（Web 工程 / MP4 / SRT / VTT 可用；音频包 / 配音 PPTX 显式门禁说明）；MP4 选项（字幕烧录复选框＋"已写入快照、服务端暂不烧录"说明＋分辨率 1920×1080·30fps 服务端固定说明＋无页面 PNG 时阻断）；保留备注元数据；快照绑定提示。ProjectEditor 导出按钮改为打开对话框。
  - "新字幕不配旧音频"约束：一次导出仅加载**单一** timeline bundle（`loadTimelineBundle`），字幕与音频同源，天然满足，无需改动。
  - i18n 中英各 22 键；styles.css 补对话框/选项/门禁/快照样式。
  - 验收：A15、A19。注：音频包/配音 PPTX 后端未提供（`exportFormat` 仅 4 种），故门禁不上线；字幕烧录编码待后续接入。本机需 `go build ./...` + `npm run build` 终验。

- **B3-M5 生成中继续编辑的顶部快照提示【已实施】**：
  - 前端（纯前端）：`ProjectEditor.tsx` 感知本项目活跃生成任务（`JobService/List` 过滤 kind∈{narration,script_draft} 且 state 属活跃态；5s 轮询，与任务中心断线回退频率一致，不在编辑器内额外持有长连接）；头部下方新增 `snapshot-banner` 提示条——「有 N 个生成任务进行中」＋快照语义说明（任务以创建时**已确认**的讲稿快照为输入，因此可继续编辑，新改动将在下一次生成生效）＋跳转任务中心入口。创建生成后与生成完成后各立即刷新一次，无需等待轮询。
  - i18n 中英文各 3 键（`editor.genInProgress` / `genSnapshotNote` / `genViewJobs`）；`styles.css` 提示条与脉冲点样式 + `theme.css`(light) 覆盖。
  - 验收：A15（与 M4 的 C-5「存在未确认稿即阻止生成」＋ `rate_percent`/`lock_confirmed_only` 实传共同覆盖 G-05）。注：本里程碑纯前端；本机需 `npm run build` 终验（沙箱无法 tsc）。

### B4 管理与适配

| 项 | 内容 |
|---|---|
| 前端 | ① 权限边界组件（导航/按钮/路由统一走服务端能力）；② 个人设置页（资料/语言/密度/通知偏好，只显示能生效项）；③ 模型服务"已配置/检测通过/当前不可用"三态 + 模拟供应商"演示模式"标识；④ 移动端单列布局与底部播放器；⑤ 首页待处理事项与最近完成成品；⑥ 任务列表补"范围/阶段/步骤/受影响页/traceId" |
| 后端 | ① 若需切租户：新增"我的租户列表"接口；② 个人偏好存储（若无则明确不做并移除入口） |
| 出口 | 按平台与角色通过验收；菜单与后端一致 |
| 验收 | A22、A23、A24、A25、A26 |

**执行计划（里程碑门控 + 验证回路）**
> 约束同 B2/B3：本环境 buf/protoc 不可用 → 新后端能力走原生 HTTP 端点；每个里程碑交付后仍需本地 `go build ./...` + `npm run build` 验证。

> **验证回路更新（2026-09-16，B4-M3 期间打通，重要）**：此前"沙箱无法 go build/tsc"的结论已被推翻，现可在沙箱内完成真实编译验证，**每个里程碑应直接跑通再提交**：
> - **前端**：`cd web && node node_modules/typescript/bin/tsc -b && node node_modules/vite/bin/vite.js build`（等价 `npm run build`）。前置：`web/node_modules` 是在 Linux 环境安装的，只含 linux 原生包，Windows 上会报 `Unable to resolve @typescript/typescript-win32-x64` / `Cannot find module '@rolldown/binding-win32-x64-msvc'` / `lightningcss.win32-x64-msvc`。修复方式二选一：本机重跑 `npm install`，或按 `optionalDependencies` 版本从 registry 下载对应 win32 包解压进 `node_modules/`（本次已就地补齐 `@typescript/typescript-win32-x64@7.0.2`、`@rolldown/binding-win32-x64-msvc@1.2.8`、`lightningcss-win32-x64-msvc@1.33.0`）。
> - **后端**：`GOPROXY=https://goproxy.cn,direct GOSUMDB=off go build ./...`（`proxy.golang.org` 走 IPv6 不可达；模块缓存原本为空，需指定可达代理首次拉取）。
> - **静态检查/测试**：`GOPROXY=https://goproxy.cn,direct GOSUMDB=off go vet ./internal/...`、`go test ./internal/...`。
> - **前置缺陷已修复（R-9，2026-09-16）**：`internal/api` 47 处 `NewHandler` 调用补 `pool` 实参、`internal/app` 测试桩补 `ListByProject`、`internal/integrations/render` 的 poppler 用例补 Skip 守卫（对齐同文件 LibreOffice 用例），另修 `internal/api/narration.go` 既存 import 乱序。**`go vet ./internal/...` 与 `go test ./internal/...` 现已全绿**（`internal/api` 45 个用例通过；14 项 Skip 全为环境依赖型：PG / ffmpeg / S3 / LLM·TTS 凭据 / LibreOffice / poppler），后续每个里程碑以此为回归基线。
> 勘察结论（2026-09-16，逐文件核实）：① 角色由 `TenantService.Members` 读取后以 props 下传（`App.tsx:104-121`），**全仓无路由守卫**，仅两处 ad-hoc 角色分支（`ProjectEditor.canReview`、`PublicAdmin.isAdmin`）；② 模型服务仅"已配置 + 本次测试"两态，后端无持久化测试状态（`0023_model_gateways.sql` 无 tested_at/status 列）；③ 无 fake provider，`web/src/mockData.ts` 为死文件；④ Player 无任何键盘处理，无底部固定播放条；⑤ 首页为 hero/统计/最近项目/最近任务，无"待处理事项/最近成品"；⑥ 任务列表缺 范围/阶段/步骤/受影响页；`job_steps` 表**已存在但无任何 RPC 暴露**，`jobs.traceparent` 存在但未进 proto；⑦ **无**"我的租户列表"RPC，**无**任何 per-user 偏好存储（migrations 无偏好表）。

- **B4-M1 权限边界组件（PermissionBoundary）【已实施】**：
  - 新增 `web/src/permissions.ts`：能力模型逐条镜像服务端 `requireRole`（`roles.go` rank：viewer0 < reviewer1 < editor2 < admin3 < owner4）；16 项能力各注明服务端依据文件行号；`can(role, cap)` 在 `role === undefined`（成员体系未配置/读取失败）时放行，与服务端 `requireRole` 的 nil-reader 放行语义一致，避免"前端隐藏、后端放行"的新不一致。
  - 新增 `components/PermissionBoundary.tsx`：`PermissionBoundary`（`ready`/`pendingFallback`/`fallback`）、`NoPermissionNotice`、`RolePendingNotice`。
  - `App.tsx`：新增 `roleReady`；**路由级守卫**（成品列表=artifact.list、成员=member.manage、审计=audit.read、模型=gateway.manage）——直接输入 URL 也只能看到统一无权页。
  - `AppShell.tsx`：设置子菜单按能力过滤；「设置」主入口与用户菜单「设置」改为落到第一个可见子页（原硬编码 `/settings/members` 对非管理员是假入口）。
  - `ProjectEditor.tsx`：`canReview`/`canEditScript`/`canGenerate`/`canExport`/`canListArtifacts` 全部改走 `can()`；无生成/导出权限时按钮禁用并给出准确说明，成品入口直接不渲染。
  - `ScriptEditor.tsx`：新增 `canEdit`——EDITOR 以下段落只读、工具栏与勾选禁用并显示只读说明。修复"Reviewer 可点保存、但服务端 `script.go:55` 要求 EDITOR → 必然 403"的越权假象。
  - `PublicAdmin.tsx`：`isAdmin` 改走 `can(role,'public.manage')`。
  - i18n 中英各 8 键（`perm.*` 7 项 + `script.readOnlyNote`）；`styles.css` + `theme.css`(light) 配套。
  - 验收：A22（菜单与后端一致 + 直接 URL/请求不能越权），同时消除 A26 的一类"假能力"。注：纯前端；本机需 `npm run build` 终验。

- **B4-M2 模型服务三态 + 演示模式标识【已实施】**（A23）
  - 落地：新增 `web/src/gatewayState.ts`（`gatewayHealth`／`serviceState`，纯函数、可测）与 `GatewaySettings.tsx` 改造——每条网关显示 **5 态健康徽标**「已停用／未配置密钥／未检测／检测通过／当前不可用」，页面顶部新增**语音合成服务可用性提示条**（未配置／当前不可用／未检测／可用），失败态附**排查建议 + 修复入口**（测试连接、编辑、接入模型）。
  - **「演示模式」的诚实落地**（重要约束）：worker 侧真实 TTS 供应商由环境变量 `PPTS_TTS_PROVIDER`（`fake`|`siliconflow`）决定（`cmd/worker/main.go:56`、`:351`），**不经任何 HTTP/RPC 端点暴露**，控制台无法得知。故不渲染 `fake` 这类内部枚举（遵循 `docs/PPT讲解平台-控制台UI设计评审.md:53/:57` 的 P1 要求），改用**可验证的等价信号**：本租户是否存在「启用 + 有密钥」的 TTS 网关。无可用网关时，提示条显式声明"生成的讲解语音与成品可能不是正式产物"，并指向「接入模型」修复入口——恰好满足 A23 的"不输出伪正式成品；提示作用与修复入口准确"。
  - **未检测 ≠ 不可用**：`model_gateways` 无 `tested_at`/`status` 列（`migrations/0023`），检测结果仅为**会话内**状态，刷新即回到「未检测」，UI 已在条目与提示条两处显式标注。
  - 附带修复：前端 `createGateway`/`updateGateway` **从不发送 `provider`** → 后端只能取默认 `openai_compatible`，`provider` 在 UI 是死数据。现补透传 + 列表展示 + 表单可编辑。
  - i18n 中英各 14 键（`gateway.health*`×5、`gateway.service*`×5、`testNow`/`providerLabel`/`untestedNote`/`fixHint`）；`styles.css` + `theme.css`(light) 配套。
  - 验收：A23。注：纯前端；沙箱不能 `tsc`，本机需 `npm run build` 终验。
- **B4-M3 无接口功能门禁收口【已实施】**（A26）：
  - **验收口径**（`设计方案-V1_6.md:509`）：无接口功能 → 隐藏或明确不可用，无可点击的假保存/假导出。
  - **逐项复核清单与结论**（本轮全仓扫描，含后端路由注册表核实）：
    | 入口 | 复核结论 | 处置 |
    |---|---|---|
    | 导出类型：音频包 / 配音 PPTX（`ExportDialog.tsx`） | 后端 `export.go:124-137` 白名单仅 MP4/WEB_PROJECT/SRT/VTT，**确认无能力** | 维持 disabled + 「已门禁」标签 + 原因文案（原 B3-M4 已正确，仅复核） |
    | 音色「试听」（`ProjectEditor.tsx:836`） | 后端无 voice catalog、无样例音频端点 | 维持 disabled + tooltip（原 B2 已正确，仅复核） |
    | 用量页「存储用量 / 租户策略」 | `TenantService.Quota/StorageUsage/Policy` **已注册**（`server.go:99`），能力存在，原问题在**前端用 `.catch(() => null)` 吞错**，把 403/500 一律渲染成"暂不可用"且无原因无重试 | **改**：逐端点记录错误 + 原因 + 重试按钮 |
    | **「我的发布」`GET /public/works/mine`** | 处理器 `public.go:341 publicListMine` **已写好但从未挂载**→ 被 `GET /public/works/{id}` 以 `id="mine"` 捕获，进 uuid 查询报 **500** | **修**：注册路由（后端能力齐备，无需改 proto） |
    | **「审核队列」`GET /public/works/queue`** | 同上，`public.go:356 publicReviewQueue` 亦未挂载；且前端 `.catch(() => {})` **完全吞错**，渲染成"暂无待审核作品"——纯假状态 | **修**：注册路由（auth + requireAdmin）+ 前端错误态/重试 |
    | 首页用量 / 存储卡片 | `.catch(() => ({secondsUsed:0}))` → 把"取数失败"伪装成"用量为 0"（误导性假数据）；且主数据请求无 catch，失败时页面用"暂无项目"掩盖 5xx | **改**：失败显示「—」+ 明确原因；主数据失败显式报错 + 重试 |
    | 匿名播放页 `Watch`（讲解清单） | `.catch(() => null)` 把 403/500 与"讲解未生成(404)"压成同一句"讲解未就绪" | **改**：按 404 与其他错误分流 |
    | 公开广场 `Explore` | `.catch(() => setWorks([]))` → 后端 500 显示"暂无作品" | **改**：空数据与加载失败可区分 |
    | 成品列表下载按钮（`ProjectArtifacts.tsx:207`） | `disabled={!a.downloadable}` **无任何说明** | **改**：补 tooltip + 行内原因文案 |
    | 审计筛选（`AuditPanel.tsx`） | 三个筛选只改本地 state、**必须另点"刷新"才生效**（观感像"已应用"的假筛选） | **改**：筛选变化自动重查（文本输入防抖 300ms） |
    | 音色兜底 `fake-voice-1` | 本租户无可用 TTS 网关时，后端仍要求 `voice_id` 非空（`narration.go:70`），前端把**内部枚举当正常音色渲染**，可据此触发正式生成 | **改**：显式标注「模拟音色（开发用）」+ 声明可能非正式产物 + 修复入口（接入模型）；不再暴露枚举名 |
    | 角色读取失败（`App.tsx`） | `.catch(() => setRole(undefined))` → `can(undefined,*)` 恒真 ⇒ 对**瞬时失败**也放宽，会闪现管理入口随后 403 | **改**：保留 `role=undefined` 的放行语义（对齐服务端 nil-reader），但**显式提示"权限判定已临时放宽"+ 重试** |
    | `web/src/mockData.ts` | 全仓零引用的死文件（`demoTimeline/demoManifest/demoScripts`），从未渲染 | **删** |
    | 禁用控件无说明（成员页 owner 行删除、编辑态成员标识、新建时 OWNER 选项、网关编辑态类型/名称） | 禁用但无原因 | **改**：统一补 tooltip |
    | `POST /public/works`（发布） | 后端**确实存在**（`public.go:37`），发布弹窗入口真实可达 | 无需处理（非假功能） |
  - **新增统一出口** `web/src/apiError.ts`：`describeApiError`（把错误码转成"明确不可用 + 原因"，替代各页 `.catch(() => null)`）、`isNotFound`（区分 404 业务语义与异常）、`settle`（把可能失败的取值变成显式结果）。约定：任何"失败会渲染成空态"的请求都必须走此出口并配重试入口。
  - **后端**：`internal/api/public.go` 注册 `GET /public/works/mine`（auth）与 `GET /public/works/queue`（auth + `requireAdmin`），两条响应统一带 `next_cursor` 以对齐前端 `PublicWorkPage` 类型。
  - **配套修复（编译阻塞）**：`c5a723e`（B3-M5）引入 `activeGenJobs` 时**漏声明 state**、`NarrationEstimate` **漏导入**，`tsc -b` 必失败；`Watch.tsx` 从 `../api` 导入了未再导出的 `PlaybackManifest`；`scopeSlideIds` 闭包内未收窄可辨识联合。均已修复。
  - **i18n**：中英各 23 键（`err.*`×5、`app.roleLoadFailed`、`public.queueLoadFailed`/`audioFailed`、`usage.storageFailed`/`policyFailed`、`home.loadFailed*`×3、`home.stat.unavailable`、`editor.simulatedVoice*`×2/`fixVoice`、`artifacts.download*`×3、`members.*`×3、`gateway.immutableWhenEditing`）；`styles.css` 新增 `.load-failure` 失败态容器与 `.narration-note.warn`，`theme.css` 补浅色覆盖（错误在浅色下也必须可读）。
  - **验收**：A26（同时消除 A22 的"闪现有权限、请求必 403"一类假能力）。附：本轮首次打通沙箱内真实验证——`npm run build`（tsc -b + vite build）与 `go build ./...` 均通过，见「验证回路」小节。
- **B4-M4 移动端单列 + 底部播放器 + 播放快捷键【待实施】**（A25）：编辑器/播放器单列化，播放器底部吸附；空格/方向键控制播放且**不抢占输入焦点**。
- **B4-M5 首页待处理事项与最近完成成品【待实施】**（配 A23/A26 语义）：首页补"待处理事项"（待确认稿、待审核作品、失败/待重试任务）与"最近完成成品"，均以真实接口为准。
- **B4-M6 任务列表补 范围/阶段/步骤/受影响页/traceId【待实施】**：步骤数据（`job_steps`）与 traceparent 已存在，可经**原生 HTTP 端点**暴露（protoc 不可用，不改 proto）；范围/阶段/受影响页服务端**完全不存在**，需新增列 + 迁移 0026，建议拆为独立里程碑并先确认口径。
- **C-6（决策待定）**：切租户本轮做 or 明确不做并隐藏入口 → 决定是否需要新增"我的租户列表"接口（后端①）。当前后端无该 RPC，维持"不做"则同步确认入口已隐藏。

### B5 可选增强（独立立项）

全局成品库、命令面板、邮箱自助注册、**用户作品公开发布与撤回**（`publicId` 不可反推、撤回即失效含 CDN 清理、删除级联失效）。发布能力不完整则入口整体不上线。

### 视觉基线专项（建议与 B1 并行，独立验收）

| 项 | 内容 |
|---|---|
| 背景 | 现实现为深色主题，V1.6 §14 要求浅灰工作区 + 白色面板 + 单一蓝紫主色；两者不可"各改一半" |
| 任务 | ① 建立设计 token（色板/字号/间距/圆角/密度）并统一落 `styles.css` 或拆分样式；② 圆角从 999px 收敛为 6~8px 控件 / 10~12px 卡片；③ 焦点可见、弹窗 focus trap 与 Esc、关闭后焦点返回；④ 状态改为图标+文字+颜色；⑤ 空格仅非输入焦点时控制播放；⑥ 对比度 4.5:1 校验；⑦ 触控 44px |
| 出口 | 视觉与可访问性基线通过评审；不因改视觉而重构技术栈 |

**决策（已定）**：保留深色为默认 + 新增浅色切换（见 C-3）。本专项聚焦"主题令牌体系 + 切换按钮 + 浅色令牌集"：先以 `theme.css` 的 `html[data-theme="light"]` 覆盖集落地浅色，后续再统一收敛为单令牌系统；不再做深→浅整体重构。

---

## 6. 需确认的决策点（阻塞 B1/B2 启动）

| 编号 | 决策点 | 可选路径 | 影响 |
|---|---|---|---|
| C-1 | 公开区内容来源 | **【已定 + 已实施】双 Tab**：①「官方精选」= 管理员在控制台上传并发布到精选目录（`POST /public/featured`，admin）；②「用户作品」= 用户主动发布（`POST /public/works` → `pending`），经管理员审核通过（`PUT /public/works/{id}/review` → `approved`）后公开展示。后端 `publications` 表 + RLS 双策略，公开只读接口仅读取 `approved` 集（`63ede93`/`cc506c7`/`279d820`/本轮管理前端）。音频播放（Watch 页）按设计延后至 B3 | B1 后端扩为：精选目录 CRUD + 用户发布/审核流；公开列表/详情只读 approved |
| C-2 | SEO 策略 | **【已定】A（元数据 + CSR）**：先落地 title/description/OG + 干净 URL（history 路由），预渲染/SSR 延后，待 SEO 需求明确再评估 | 本轮不引入预渲染与分离构建；公开区可发现性以元数据满足 A27 |
| C-3 | 主题基线 | **【已定】保留深色为默认，新增浅色切换**：顶栏右上角太阳/月亮按钮切换深/浅；浅色以浅灰工作区 + 白色面板 + 蓝紫主色为基调（非全量改 §14） | 视觉基线专项改为"主题令牌 + 切换"，不再做深→浅整体重构 |
| C-4 | 路由形态 | **【已定 + 已实施】A（history + 服务端 fallback）**：自研 hash 路由已改为 History API（`b96352b`）；dev 由 Vite SPA fallback 兜底，prod 由 Go 后端 catch-all 经 `PPTS_WEB_ROOT` 返回 index.html（`e1e6169`）；遗留 `#/path` 深链接在挂载时改写为 `/path` | 公开区可访问性与所有既有链接需重测；B1 其余项待做 |
| C-5 | 生成口径 | 是否强制"未确认稿禁止正式生成"（A09） | 影响 B2 出口条件与用户流程 |
| C-6 | 切租户 | 本轮做 or 明确不做并隐藏入口 | 决定是否需新增租户列表接口 |
| C-7 | 个人设置 | **【已定】明确不做并移除入口**：遵守 §1.2 第 3 条；若设置菜单已有个人设置入口则隐藏，本轮不实现个人设置页（留作后续 B4） | B4 ④ 顺延；当前不暴露虚假能力 |
| C-8 | 公开作品发布 | 是否纳入本轮（P2） | 决定 B5 与 A29 |

---

## 7. 风险登记

| ID | 风险 | 概率 | 影响 | 应对 |
|---|---|---|---|---|
| R-1 | 手写 fetch 未用生成客户端，字段漂移持续发生（已发现 `rate_percent` 漏传） | 高 | 中 | B0 引入生成 Connect 客户端或建立契约测试；关键 RPC 加 e2e 断言 |
| R-2 | 匿名接口若复用租户上下文接口，会导致字段越权泄露 | 中 | 高 | 强制独立最小字段集 + 独立路由；对匿名接口单独写越权测试 |
| R-3 | 主题从深转浅牵动全部页面，与业务批次并行易冲突 | 高 | 中 | 视觉专项独立分支，先落 token 再逐页替换 |
| R-4 | 公开区预渲染与控制台纯 CSR 混用带来构建复杂化 | 中 | 中 | 按 C-2 决策，默认先做元数据版本，SEO 需求明确后再预渲染 |
| R-5 | 播放器真实音频与时间轴对齐误差（长片门槛沿用 V4.0 实测） | 中 | 高 | D0-5 之后立即做一次真人试听验收，记录偏差 |
| R-6 | 后端 `RegenerateSegments` 未实现成为 B2 关键路径 | 高 | 中 | 排在 B2 首位；若延期则暂以"整页重生成 + 明确影响范围"降级并标注 |
| R-7 | 工作区存在大量未提交改动，与并行开发冲突 | 高 | 中 | 开工前先提交/整理当前控制台改动并建分支 |
| R-8 | A04/A06 类口径与作用域缺陷易被反复遗漏 | 中 | 中 | 把 A01–A29 打成验收清单，每批次出口逐条勾选 |
| R-9 | ~~后端测试包长期不可编译~~ **【已解决 2026-09-16】**（`NewHandler` 于 `63ede93` 加 `pool` 形参后 `server_test.go` 未同步；B3-M1 新增 `artifact.Store.ListByProject` 后测试桩未实现） | 已发生 | 高 | 因 `go build` 不编译 `_test.go`，长期未被发现；B4-M3 期间经 `go vet` 暴露。**修复完成**：47 处调用补 `nil` 实参（`server_test.go` 45 + `gateway_test.go` 1 + `e2e_test.go` 1）、`internal/app` 桩补 `ListByProject`、poppler 用例补 Skip；`go vet` + `go test ./internal/...` 全绿 |
| R-10 | 里程碑交付只做"单文件类型复核+`gofmt` 兜底"就提交，缺陷（编译阻塞、吞错假状态、未挂载路由）会跨里程碑累积 | 高 | 高 | 按「验证回路更新」在沙箱内跑通 `tsc -b` + `vite build` + `go build ./...` + `go vet ./internal/...` 后才提交；新增后端路由须核对**注册表**而非仅核对处理器函数 |

---

## 8. 验收门控（每批次出口必须逐条勾选）

| 批次 | 覆盖条款 |
|---|---|
| D0 | A01、A02、A04、A10、A11、A21 |
| B1 | A01、A03、A05、A07、A27、A28 |
| B2 | A08、A09、A10、A11、A12、A13、A14 |
| B3 | A15、A16、A17、A18、A19、A20、A21 |
| B4 | A22、A23、A24、A25、A26 |
| B5 | A29 |

验收一律使用真实 PPT、真实 TTS 与真实异步任务；模拟数据仅用于组件状态展示，需显式标注，不作为功能验收证据。

---

## 9. 建议的启动动作（本文交付后即可执行）

1. **先定 §6 的 C-1/C-3/C-4 三项**——它们决定 B1 与视觉专项的形态，其余可后置。
2. **并行启动 D0**——9 项确定性修复不依赖任何决策，可立即开工，且直接改善 6 条验收。
3. **B0 差异表落地**——把本计划 §2 的对照矩阵推进到字段级，并把 `rate_percent` 这类"proto 已定义、前端漏传"问题一次性扫干净。
4. **整理当前未提交改动**——25 个文件处于修改态，建议先提交或归档再开新批次（R-7）。

---

## 附：本计划的核对依据（代码证据索引）

| 主题 | 位置 |
|---|---|
| 未登录直接进登录页、无公开区 | `web/src/App.tsx:85-91` |
| 根路径与未知路由行为 | `web/src/App.tsx:146-152` |
| hash 路由实现 | `web/src/router.tsx:9-32` |
| 全部 RPC 强制鉴权 | `internal/api/server.go:71-97` |
| 确认/锁定已实现 | `internal/api/script.go:75,93`、`proto/ppts/v1/script.proto:16` |
| 局部重生成未实现 | `internal/api/narration.go:38`、`gen/ppts/v1/narration.pb.go:288` |
| 产物仅有 Create/Get | `internal/artifact/model.go:42` |
| 生成入参漏传 | `web/src/api.ts:175-214` vs `proto/ppts/v1/narration.proto:32-38` |
| 播放器模拟时钟 | `web/src/Player.tsx:19-36` |
| 音频资源类型已定义未使用 | `web/src/types.ts:36` |
| 开发身份门控 | `web/src/auth.ts:24-27` |
| 首页统计口径 | `web/src/pages/Home.tsx:69-81,108-109` |
| 项目列表仅有表格 | `web/src/pages/Projects.tsx:152-203` |
| 成品页无产物列表 | `web/src/pages/ProjectArtifacts.tsx:112-117` |
| 保存态缺 error/conflict | `web/src/ScriptEditor.tsx:15,67` |
| 深色主题与样式残留 | `web/src/styles.css:2-6`、`:19-22` |
| 页面标题与元数据 | `web/index.html:5` |
| i18n 无公开命名空间 | `web/src/i18n/zh.json`（451 键，无 public/showcase） |
| 后端能力总览 | `README.md:43-50,96-108` |
| 渲染产物路径 | `README.md:48`（`{tenant}/{project}/src-NN/render/page-NNNN.png`） |
