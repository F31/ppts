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
| 开发身份仅显式门控 | 已改为仅 `DEV`（或构建期显式 `VITE_ALLOW_DEV_IDENTITY=true`）开放，且**读侧同步门控**（D0-2 ✅） | ✅ A01 |
| 正式环境未配认证 → "登录服务尚未配置"且不回退 | 生产构建渲染 `login.unconfigured`，不再回退到开发身份表单（D0-2 ✅） | ✅ |
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
| 中文输入法组合期不提交 | 组合期不调度、不提交（`composingRef` 守卫 + 单调度器，D0-7 ✅） | ✅ A10 |
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
| 列表列：项目/类型/提交时间/**范围**/**阶段**/进度/状态 | **已补"范围""阶段"**（B4-M6a）；项目仍显示 id 前 12 位 | ✅ |
| 详情：步骤(JobStep)/受影响页/输入版本/traceId | **已补**（B4-M6a，原生 HTTP `GET /jobs/{jid}/detail`） | ✅ |
| 列表按 阶段/受影响页 **排序与筛选** | **已补**（B4-M6b，原生 HTTP `GET /jobs/page` + 列表筛选/排序控件，条件写 URL） | ✅ |
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
| 浅灰工作区 + 白色面板 + 单一蓝紫主色 | 已实施**深色主题**（V-M1 设计令牌体系，`#10131a` + 青 `#67e8f9`）；基线 §14 浅色要求经决策 C-5 明确保留深色为产品决策 | ✅（C-5） |
| 圆角 控件 8px / 卡片 12px / 徽标保 pill | 已收敛（V-M4：脚本化三档半径） | ✅ |
| 字号/间距/密度基线 | 已建立 token 体系（V-M1：色板/字号/间距/圆角/密度落 `styles.css` `:root` + `theme.css` 浅色重定义） | ✅ |
| 状态=图标+文字+颜色 | 已实现三重编码（V-M4：`--glyph` + `::before` 注入语义图标，颜色+图标+文字） | ✅ |
| 键盘：可见焦点/弹窗焦点管理/Esc/焦点返回 | 已实施（V-M3：`:focus-visible` 焦点环 + `useDialogA11y` focus trap/Esc/焦点返回，接线 7 处 dialog） | ✅ |
| 播放快捷键（空格/方向键不抢占输入） | 已实施（B4-M4：播放快捷键，非输入焦点时不抢占） | ✅ |
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
| D0-2 | 开发身份门控 | `auth.ts`（`isDevIdentityEnabled`/`storedDevIdentity`） | 改为"仅 `import.meta.env.DEV` 或构建期显式 `VITE_ALLOW_DEV_IDENTITY=true` 开放"；**读侧同步门控**（残留 localStorage 的 dev identity 不被采纳）；生产构建未配 OIDC 时只渲染"登录服务尚未配置"、不回退 | A01、A02 |
| D0-3 | 首页项目总数口径 | `Home.tsx:108` | 改用真实聚合能力或标"暂无统计"；统计卡标签写明计数对象 | A04 |
| D0-4 | 可播放项目统计失真 | `Home.tsx:69-81,109` | 明确"最近 N 项中可播放数"或改由聚合接口给出 | A04 |
| D0-5 | 播放器无音频 | `Player.tsx:19-36` | 引入 `<audio>` 播放 manifest 中 AUDIO 资源，进度以 `timeupdate` 为准；保留无音频降级 | A21 |
| D0-6 | 保存态缺 error/conflict | `ScriptEditor.tsx:15,67` | 五态显示；失败/冲突保留输入并可重试 | A11、A12 |
| D0-7 | 中文输入法组合期提交 | `ScriptEditor` 保存调度 | `compositionstart/end` + 组合期不调度；**保存收敛为单一定时器**（原防抖 effect 的定时器与组合结束的定时器互相不可见，组合确认时会并发两次提交同一 revision） | A10 |
| D0-8 | 公共可发现性基线 | `web/index.html` | 标题改"智讲 PPT"、补 `description`/OG 标签、`lang` 与 favicon | A27 |
| D0-9 | 死代码与样式残留 | `styles.css` / `theme.css`：`.workspace`/`.slide-rail`/`.project-panel`/`.panel-row`/`.create-project`/`.project-list`/`.asset-panel`/`.preview-column` | 清理 `ProjectPanel.tsx` 时代的残留样式（含**共享规则内的死选择器**、`1040px`/`720px` 媒体查询里的死规则、`theme.css` 浅色覆盖）；**保留 `.rail-title`**（`slide-rail-v2` 复用同名类，仍生效） | 工程质量 |

**D0 批次状态（2026-09-16 收口完成）**：**9 项全部完成 —— D0 批次归零**。D0-1 ✅（B3-M4）、D0-5 ✅（B3-M2）、D0-6 ✅（`ScriptEditor` 五态 `saved/dirty/saving/error/conflict`）、D0-8 ✅（`index.html` 已含 `lang`/`title`/`description`/OG，仅缺 favicon）、D0-3 ✅ / D0-4 ✅（B4-M5）、D0-2 ✅ / D0-7 ✅ / D0-9 ✅（本日「D0 收口」，见 B4 章节）。
> **复核纠正**：D0-7 原记为"`ScriptEditor.tsx` 无 `compositionstart/end` 处理"**与仓库不符**——该处理在 B2-M2（`4192e21`）已加入（`composingRef` 守卫）。真实缺陷是**保存定时器不唯一**：防抖 effect 的 500ms 定时器与 `onCompositionEnd` 另起的 720ms 定时器互不可见，中文输入法确认时会并发两次提交同一 `expectedRevision` → 服务端冲突。本轮按 A10 收口为单调度器。

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

**决策依赖**：C-1（公开区双 Tab）、C-2（SEO 元数据 + CSR）、C-3（深色默认 + 浅色切换）、C-4（history + 服务端 fallback）**均已拍板并实施**（见上方 callout 与各 C-* 行状态）。**C-5/C-6/C-7 亦已定案**（C-5 强制阻止未确认稿·已实施；C-6 明确不做切租户；C-7 明确不做个人设置），§6 决策点表已同步；C-8 延后至 B5 立项。分离构建按 C-2 维持「暂不做」。

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
> 勘察结论（2026-09-16，逐文件核实）：① 角色由 `TenantService.Members` 读取后以 props 下传（`App.tsx:104-121`），**全仓无路由守卫**，仅两处 ad-hoc 角色分支（`ProjectEditor.canReview`、`PublicAdmin.isAdmin`）；② 模型服务仅"已配置 + 本次测试"两态，后端无持久化测试状态（`0023_model_gateways.sql` 无 tested_at/status 列）；③ 无 fake provider，`web/src/mockData.ts` 为死文件；④ Player 无任何键盘处理，无底部固定播放条；⑤ 首页为 hero/统计/最近项目/最近任务，无"待处理事项/最近成品"；⑥ 任务列表缺 范围/阶段/步骤/受影响页；`job_steps` 表**已存在但无任何 RPC 暴露**，`jobs.traceparent` 存在但未进 proto（→ **B4-M6a 已以原生 HTTP 端点暴露，见 §5；且范围/受影响页/输入版本经核实已存于 `input_snapshot`，无需迁移**）；⑦ **无**"我的租户列表"RPC，**无**任何 per-user 偏好存储（migrations 无偏好表）。

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
- **B4-M4 移动端单列 + 底部播放器 + 播放快捷键【已实施】**（A25）：
  - **播放快捷键**（`Player.tsx`）：空格 播放/暂停、← → 快退/快进 5 秒、↑ ↓ 上一页/下一页。**不抢占输入**——焦点位于 `input`（含进度条滑块）/ `select` / `textarea` / `button` / `a` / `contenteditable` 时一律让位，对齐 V1_6 §397「空格仅在非文本输入焦点时控制播放；方向键不抢占文本编辑」。用 latest-ref 承载最新状态，监听器只挂一次（避免 rAF 每帧重挂）；播放器内新增快捷键说明行。
  - **底部播放器**（`styles.css`）：控制条 + 进度条收进 `.player-dock`；<768px 时 `position: fixed` 吸附视口底部（含 `env(safe-area-inset-bottom)`），卡片补 `padding-bottom` 避免遮挡；`index.html` 加 `viewport-fit=cover` 使安全区生效；桌面端为 `sticky`。
  - **响应式断点对齐设计稿**（V1_6 §379）：≥1200px 三栏；768–1199px 缩略图横排 + 两区；<768px 单列 + 底部操作（原为 1280/1040/860 三档）。触控目标按 V1_6 §395 收紧到 ≈44px（播放控制、页选择、主导航、顶栏按钮）。
  - **浅色主题**：`theme.css` 为 `.player-dock`/`.player-shortcuts` 补浅色覆盖，避免底部吸附条在浅色下变深色（A26 同源要求：错误与控件在浅色下也要可读）。
  - **i18n**：中英各 2 键（`player.dock`、`player.shortcutsHint`）。验证：`tsc -b` + `vite build` 通过。
- **B4-M5 首页待处理事项与最近完成成品【已实施】**（含 D0-3/D0-4 收口）：
  - **首页顺序对齐 V1_6 §6.1**：hero → 最近项目 → 待处理事项 → 紧凑统计 → 最近完成成品 → 最近任务（原为 hero → 统计 → 最近项目 → 最近任务，统计占据首屏，与 §6.1 及《UI 设计评审》"首屏应以'继续工作'为主、统计降权"相反）。hero 补「新建讲解」CTA，按 `can(role,'project.create')` 门控（服务端 `project.go:35` 要求 editor）。
  - **待处理事项**（V1_6 §6.1-3，全部真实接口、无数据则不渲染区块）：
    | 待办类型 | 数据来源 | 权限 |
    |---|---|---|
    | 讲稿待确认（N 段） | `GET /projects/{pid}/narration/draft-count`（`getNarrationDraftCount`） | editor（`narration.go:339`） |
    | 音频需更新（N 页） | `GET /projects/{pid}/narration/stale`（**新增 `getNarrationStale`**）——该端点自 B3-M1 起就在 `registerNarrationRoutes` 挂载（`narration.go:328`），但**前端从未调用**，本次接入；后端判定 `audio_revision < revision` | editor（`narration.go:358`） |
    | 任务失败 / 待重试 | `JobService.List` 中 `JOB_STATE_FAILED`（可**就地重试**，`RetryFailed` 要求 editor）与 `JOB_STATE_RETRY_WAIT` | 任务读取仅需已认证 |
    | 待审核作品（N 个） | `GET /public/works/queue`（B4-M3 刚挂载的路由，此处首次真正消费） | admin（`public.go:333`） |
    每项均带跳转（编辑器 / 任务详情 `?job=` / `/settings/public`）。注意 `publicListMine`/`publicReviewQueue` 后端**不加 LIMIT、不分页**（`internal/public/store.go:150,171`），故其 `items.length` 是精确条数，可直接展示。
  - **最近完成成品**（V1_6 §6.1-5）：`GET /projects/{pid}/artifacts`（B3-M1 端点）跨最近 6 个项目聚合，按 `createdAt` 倒序取前 5，展示格式徽标 / 项目名 / 大小 / 导出时间，点击进入该项目成品页（`/projects/{id}/artifacts`，同样要求 editor）。有成品才渲染。
  - **统计口径修复（D0-3 + D0-4，A04）**：
    - D0-3：`ListProjectsResponse` **无总数**（`proto/ppts/v1/project.proto:68` 只有 `projects` + `next_cursor`），原实现把 `pageSize=20` 的**本页长度当"讲解项目"总数**。新增 `listProjectsPage`（带游标）：**游标为空（列表完整）才显示精确数**并注明"完整列表"；游标非空则显示 `≥ N`，并写明"仅最近一页 N 项·服务端不提供总数"。
    - D0-4：「已配音项目」原实为**最近 6 项**中的数量却按全租户口径展示；现标签下写明"仅统计最近 {count} 个项目"，且非 editor 时该卡显示「—」+「需编辑及以上角色」（`narration.read` 门控），不再伪造 0。
    - 附：用量显式传 **UTC 自然月**并展示周期（`getUsage` 原缺省依赖服务端 `monthRange` 的 UTC 兜底，`internal/usage/postgres.go:271`）；存储按 §202 标"取数于 {时刻}"（后端 `StorageUsage` **无统计时间字段**，见 R-12，不伪造）。模型服务卡片：非 admin 读不到时原显示"未配置"（把无权限伪装成没配），改为「—」+ 原因（A26 同源问题，与 B4-M2 一致）。
  - **富化窗口与降级口径**：待办/可播放/成品只探测**最近 6 个项目**。服务端无「首页摘要」聚合接口（V1_6 §419 列为可选能力且"不遍历全量分页强算"），故按 §202「未取得聚合接口时展示真实最近列表」降级，并在待办区与成品区**显式标注统计范围**；首屏 `statsNote` 也改写为"各项已标注统计范围"。
  - **A26 一致性**：全部富化请求走 `settle` → `describeApiError`（**无一处 `.catch(() => null)`**），失败原因去重后渲染在 `.load-failure.load-failure-stack` 中并附重试。角色未就绪（`roleReady=false`）时不下发角色受限请求——避免 A22「先闪现越权入口、再收 403」。
  - **i18n**：中英各 26 键（`home.todo*`×13、`home.stat.*`×8、`home.newNarration`/`home.statsAria`/`home.atLeast`、`home.recentArtifacts`/`home.artifactsScope`/`home.artifactOpen`、`home.probeFailed`/`home.unknownProject`），并**删除已无引用的 `home.stat.projectsNote`**；中英键数校验 687/687 完全对齐。样式 `styles.css` 新增 `.todo-*`/`.home-artifact-*`/`.home-cta`/`.warn-note`/`.load-failure-stack` 与窄屏单列，`theme.css` 补浅色覆盖（**注意类名不能沿用成品页已有的 `.artifact-row`，否则覆盖其 flex 布局——已改为 `.home-artifact-row`**）。
  - **验证**：`tsc --noEmit` + `tsc -b` + `vite build` ✅；`go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅（本轮无后端改动，仍跑以保持基线可信）。
- **D0 收口：D0-2 开发身份门控 + D0-7 输入法组合期提交 + D0-9 样式残留清理【已实施】**（补齐 R-11，**D0 批次就此归零**）：
  - **D0-2（A01/A02，安全项，最高优先）** —— `web/src/auth.ts`、`web/src/vite-env.d.ts`：
    - `isDevIdentityEnabled()`：`!oidcConfigured() || import.meta.env.DEV` → `import.meta.env.DEV || import.meta.env.VITE_ALLOW_DEV_IDENTITY === 'true'`。生产构建默认关闭；确需开发身份（如离线端到端验收）必须在构建时显式声明 `VITE_ALLOW_DEV_IDENTITY=true`，使"开放"成为一次**可审计的显式决定**，而非"未配置 OIDC"的隐式回退。类型声明补进 `vite-env.d.ts`（原 `vite/client` 的索引签名会让任意 `VITE_*` 静默通过）。
    - **读侧同步门控**（`storedDevIdentity()`）：未开放该能力的构建里，**即使浏览器残留 `pptsDevIdentity` 也不采纳**。否则 `App.tsx:31-41` 会把它当身份，经 `api.ts:43-46` 发出无 token 的 `X-PPTS-Tenant-ID`/`X-PPTS-User-ID`——这正是 A02「测试身份不混入生产」要堵的后门（只改 UI 门控不足以堵住它）。
    - 登录页无需改动：`Login.tsx:83-84` 的 `!showOIDC` 分支在 `showDev=false` 时本就渲染 `login.unconfigured`（"登录服务尚未配置，请联系管理员。"），满足 A02「提示配置问题且不回退」。
    - **产物级双向验证**（`vite build` 后直接读 `dist/assets/index-*.js`）：默认生产构建门控折叠为 `function Mt(){return!1}`（**恒关闭**）；以 `VITE_ALLOW_DEV_IDENTITY=true` 构建则折叠为 `return!0`（开放）。同时确认产物中 `import.meta.env` 字面**零残留**，证明 env 访问全部被构建期静态替换、不存在运行期回退。
  - **D0-7（A10，输入法）** —— `web/src/ScriptEditor.tsx`：
    - 保存时序收敛为**单一入口 `scheduleSave` + 唯一句柄 `saveTimerRef`**：任何触发点（编辑、组合结束）都先清掉上一个待提交任务；`composingRef` 为真时**绝不提交且不重排**（由 `onCompositionEnd` 统一补一次）；`saving` 态按 `SAVE_RETRY_MS=300` 退避重排（本次编辑不丢），`saved/error/conflict` 不重复提交（保持"error 需人工重试"原语义）。防抖由原 500+220 两段式改为单段 `SAVE_DEBOUNCE_MS=700`。
    - `editSegment` 在组合期不调度；`flush`（Ctrl+S / 切页前）在组合期直接返回，避免把拼音半成品写进讲稿；复位 effect（`slideId`/`revision` 变化）增加 `clearTimeout` + 清组合态，防止旧稿的待提交任务写进新页。
  - **D0-9（工程质量）** —— `web/src/styles.css`、`web/src/theme.css`：
    - 清除 `ProjectPanel` 时代（`0486203` 之前）三栏布局的残留样式：`.workspace`（含 `1040px`/`720px` 两个媒体查询里的死规则——`1040px` 块删空后整体移除）、`.slide-rail`（及其 `button`/`hover`/`span`/`em` 与 `.slide-rail .project-list button` 组合选择器）、`.project-panel`、`.panel-row`、`.create-project`、`.project-list`、`.asset-panel`、`.preview-column`；`theme.css` 同步删除对应浅色覆盖（`.slide-rail`×3、`.create-project input`、`.project-list small`、`.asset-panel`）。
    - **共享规则只摘选择器、不删整条**：`styles.css:21` 的卡片外观同时服务 `.editor-card`/`.player-card`/`.audit-panel`（均在用），因此只摘除 `.slide-rail`/`.asset-panel`/`.project-panel` 三个选择器；`theme.css:135-137` 同理。
    - **必须保留 `.rail-title`**：`ProjectEditor.tsx:676` 的 `slide-rail-v2` **复用同名类**，故 `styles.css:23` 与 `720px` 媒体里的 `.rail-title { display: none }` 仍然生效 —— **这类清理不能按块删除，要按"类名是否被 v2 复用"逐个核对**（这是本轮唯一的真实误删风险点）。
    - 核对方法：对每个候选类名在 `web/src` 全量（含 `.ts`/`.tsx` 与模板串拼接）验证无引用，另做一次宽口径 `className=[^>]*(panel|column)` 扫描排除拼接式用法；改后 `grep` 复检，仅余 `-v2` 活类。
    - **产物级验证**：`vite build` 成功且 CSS 体积 **51.44 kB → 49.06 kB**（gzip 9.94 → 9.59 kB），证明删除生效且样式表语法有效。
  - **R-13（提交在途期间的编辑被静默丢弃）修复【已实施】**（D0 收口时登记、2026-09-16 单独收口） —— `web/src/ScriptEditor.tsx`：
    - **缺陷链**：`saving` 期间用户继续输入 → `runCommit` 成功回调 `onChange(saved)` 推进 `revision` → 复位 effect 无条件 `setTexts(initialTexts(script))` + `setSaveState('saved')` → 用户新输入被服务端文本覆盖、且保存状态被置回 saved（既无"未保存"提示也无恢复入口，属 A11/A15 语义）。
    - **修复①「复位」区分两件事**：新增 `slideIdRef`，`slideId` 变化（切页）才重置选择集 / 待提交定时器 / 组合态 / `keepLocalRef`；`revision` 推进（服务端落库）时，若本次落库正是"在途期间有更新编辑"的那次提交（`keepLocalRef` 标记）或本地仍有未提交编辑（`dirty`/`saving`），则**保留本地文本**，交给其自身的提交继续推进 revision。
    - **修复②「在途」判定不再复用 `saveState`**：新增 `inFlightRef`（**按 `slideId` 记名**，而非布尔）。用户继续输入会把 `saveState` 置回 `dirty`，旧实现据 `saveState === 'saving'` 判断在途 → 实为「在途期间仍会以同一 `expectedRevision` **并发提交**」，服务端必判 conflict（`ProjectEditor.tsx:324-331` 会弹冲突框）。现 `scheduleSave` 与 `flush`（Ctrl+S/切页前）统一改为在途时退避 `SAVE_RETRY_MS` 重排，等 `props.revision` 跟上再提交。按 slideId 记名还使切页后旧页的在途请求不再阻塞新页自动保存。
    - **修复③「提交返回」识别新编辑**：新增 `editSeqRef` 编辑序号，提交发出时记下、返回时比对：序号已变 → 不置回 `saved`，改置 `dirty` 并立即重排一次提交（此时 revision 已是服务端新值，不会自撞 conflict）。
    - **`error` 仍走对齐分支（有意保留）**：冲突对话框的「采用服务端版本 / 以最新版本重试」（`ProjectEditor.tsx:431-444`）正是依赖这次对齐把服务端文本写进编辑器，故保留条件只覆盖 `dirty`/`saving`、**不含 `error`**。
    - **顺带修掉相邻缺陷**：同页 `revision` 推进不再清空段落选择集（旧实现每次自动保存都会清掉用户为工具栏选中的段落）。
    - **验证**：`tsc -b` + `vite build` ✅ + `go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅。该缺陷无自动化 UI 测试覆盖（仓库无前端测试基建），修法以"状态机三条不变式"逐条对照源码复核：①切页必重置；②服务端落库不得覆盖未提交编辑；③同一 `expectedRevision` 不得并发提交。
    - **第二轮收口（2026-09-16）—— 「提交在途 + 用户切页」路径仍会静默丢失（R-13 的覆盖缺口）**：按不变式逐条对照源码复核时发现第一轮修法的漏网路径 —— ① `flush()` 在该页已有提交在途时只是**排一个退避定时器**（`SAVE_RETRY_MS`），而紧随的 `setActiveSlideID` 让 reset effect 走切页分支，那里的 `window.clearTimeout(saveTimerRef.current)` 把这个定时器**清掉了**；② 切页分支同时 `setTexts(initialTexts(script))` 用新页文本覆盖 `texts`。于是"在途期间对原页的编辑"既没被提交、也没回到父级 —— **永久静默丢失**（命中窗口 = 提交 RTT，用户"打完字顺手点下一页"即可复现）。同源副作用还有两处：切页后旧回包仍会 `setSaveState('dirty')` 把**新页**状态改脏、并因此对新页发起一次多余提交；`commit` 通道闭包 `activeSlideID`，**根本无法提交非当前页**，故原页草案即使想续传也做不到。
    - **第二轮修法（按"页"重构保存机）**：① **未落库草案按 slideId 记名**（`draftsRef`：段落快照 + 编辑序号），每次编辑即快照，切页不再销毁；② **提交通道改为 `commit(slideId, segments, expectedRevision)`**（父级 `commitRealScript` 不再闭包 `activeSlideID`）→ 原页草案可在用户切走后继续提交；③ **每页各一个**待提交定时器 + `inFlightRef` 按页记名，切页**不再清除原页定时器**，该页在途时一律退避重排；④ 所有 `setSaveState` 用 `displayedSlideIdRef` 守卫 —— 只有"仍在前台"的页才改状态，跨页回包不再污染新页；⑤ `keepLocalRef`（一次性标记）退役，改为"**本页是否仍有草案**"这一可直接判定的事实：有草案 = 保留本地文本，无草案 = 允许服务端文本对齐（冲突面板的「采用服务端版本」仍依赖它）；⑥ 冲突改用哨兵 `ScriptConflictError` 与其它错误分流 —— 冲突**放弃草案**（本地文本已由父级存进 `conflict.localText`，两条出口都在父级，编辑器不得自动重放），网络类错误**保留草案**待人工重试；⑦ `flush()` 改为遍历**全部页**的草案（Ctrl+S 与切页前都覆盖原页），`isDirty()` 计入非当前页草案，父级 `beforeunload` 一并读它。
    - **第二轮不变式复核（逐条对照源码）**：①切页必重置**显示态**（组合态/选择集）但**不得销毁原页草案**；②服务端落库不得覆盖未落库草案（判定依据 = 草案存在性，不再嗅探会被跨页回包误置的 `saveState`）；③同一 `expectedRevision` 不得并发提交（每页至多一个在途，且 `revisionsRef` 按页 **max 单调**推进，避免"提交已成功但父级尚未重渲染"的窗口把 `expectedRevision` 写回旧值而自撞冲突）。
    - **第二轮验证**：`tsc -b` ✅ + `vite build` ✅（55 modules；CSS 49.64 kB 不变）+ `go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅（26 包全绿）。仍无自动化 UI 测试（仓库无前端测试基建），故以"状态机不变式 + 四条时序（切页/回包/冲突/卸载）逐路径推演"复核；该缺口已登记为 R-14 的教训条目（**勿把"文档已标已解决"当成"代码已无同类残留"**）。
    - **已知残留（未修，超出 R-13 范围）**：编辑器组件**卸载**（非浏览器关闭，而是应用内路由离开）时草案表随组件销毁 —— 该路径没有任何提交动作，属"离开页面未保存提示"的独立风险（`beforeunload` 只覆盖浏览器级离开）。需与前端路由守卫（或父级离开确认）一并设计，本轮不动。
  - **验证**：`tsc -b` + `vite build` ✅（55 modules；D0-9 后 CSS 49.06 kB）；`go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅（本轮无后端改动，仍跑以保基线可信）。
- **B4-M6a 任务列表/详情补 范围/阶段/步骤/受影响页/traceId【已实施】**（B4-⑥ 的可实施部分；“范围/受影响页服务端完全不存在”的旧结论已更正）：
  - **更正勘察结论（R-8「缺失项要按文件核实」）**：原记录称“范围/阶段/受影响页服务端**完全不存在**，需新增列 + 迁移 0026”。**逐字段核实后不成立** —— 任务快照 `input_snapshot` 已含全部所需信息：

    | 展示项 | 权威来源 | 说明 |
    |---|---|---|
    | 范围（kind / 页数 / 受影响页） | `NarrationSnapshot.Slides[]`+`SegmentIDs`、`ScriptDraftSnapshot.SlideIDs[]`、`ParseSnapshot.RevisionNo`、`ExportSnapshot.Format`+`PagePNGKeys` | 按任务种类解析快照即得，**无需新列** |
    | 输入版本 | 上述快照的 `RevisionNo` / `ScriptRevision` | 同上 |
    | traceId | `jobs.traceparent`（迁移 `0018_job_traceparent.sql`） | 列已存在 |
    | 步骤 | `job_steps` 表（迁移 `0001_init.sql:74-85`） | 表已存在但此前无任何端点暴露 |

    - **真正缺“列”的只有“阶段”**：库中无 phase 字段，故由 `job_steps` **最近更新的步骤类型**推导（`max(updated_at)` 对应的 `step_type`）；无步骤时留空、界面显示「—」而不伪造。
    - 结论：**M6a 不需要迁移 0026**。“阶段/受影响页”升为**服务端持久化独立列**由 **M6b 落地（见下，已实施）**；M6a 已非“前端无数据可用”。
  - **后端（原生 HTTP，protoc 不可用、不改 proto）** —— 新增 `internal/api/jobdetail.go`，在 `server.go` 的 `registerEditorRoutes` 之后挂载：
    - `GET /jobs/{jid}/detail` → `{jobId, kind, traceId, scope, steps[], stepCounts, stepTotal, stepsTruncated, stepsError?}`。
    - `GET /jobs/summary?ids=a,b,c` → `{jobs:{<id>:{scope, phase, stepTotal, stepCounts}}, stepsError?}`（列表页**单次批量取**，避免 N+1）。
    - 上限：`maxJobSummaryIDs=100`、`maxJobSteps=500`、`maxScopePages=200`；步骤超限保留**最近** N 条并置 `stepsTruncated=true`（`stepCounts`/`stepTotal` 仍为全量），受影响页超限则截断列表但 `pageCount` 保持完整（界面据 `pageCount > len(affectedPages)` 提示“已截断”）。
    - **不透内部对象键**：每步只回 `stepType/state/updatedAtUnix/hasResult`，`result_ref`（内部对象键）不外泄；`ExportSnapshot.PagePNGKeys` 只计数、不返回键。
    - **权限与 `JobService.Get/List` 同级（仅需已认证）**，不加角色门禁 —— 避免 viewer「能进列表、点详情必 403」的假能力（A22）；理由已写入源码注释。
    - **A26 降级**：store 未实现 `JobInspector` → `stepsError:"unsupported"`（501）；步骤读取失败 → `stepsError:"load_failed"`（部分数据仍返回，不压成空态）。
    - `internal/pipeline`：`PGStore` 新增 `GetMany`/`ListSteps`/`StepSummaries`；新增 `JobStepSummary{Phase,Total,Counts,LastAt}`；**`Get` 现包裹 `ErrJobNotFound`**（此前未包裹 → api 侧 `jobError` 映射成 500，修为 404）。
    - 修复 `writeConnectError`（`narration.go`）缺失的 `CodeUnimplemented → 501` 映射（此前返回 500）；该缺口由新测试 `TestPublicJobsSummaryUnsupportedStore` 暴露。
  - **前端** —— `web/src/pages/Jobs.tsx`（列表 + 详情面板）：
    - 列表新增「范围」「阶段」两列，经 `getJobsSummary` 按当前页 ID 批量取；取数失败显式报错 + 重试（`.load-failure`），**不落成空白单元格**；`stepsError` 单独提示原因。
    - 详情面板新增「范围 / 受影响页 / 输入版本 / traceId」与「执行步骤」表格；原始 `inputSnapshot` 改为可折叠 `<details>`（调试/审计用）。
    - 步骤不可用区分 `unsupported`（无重试，能力未实现）与 `load_failed`（带重试）——A26。
    - 详情重取用请求序号 `detailSeqRef` 丢弃过期响应（快速切任务 / 连点重试时，慢响应不得覆盖新结果）。
    - `web/src/api.ts` 新增 `getJobDetail`/`getJobsSummary` 及 `JobStep`/`JobScope`/`JobExtras`/`JobDetail` 类型；`web/src/types.ts` 新增 `jobStepTypeKey`/`jobStepStateKey`/`jobScopeKindKey`（`step_type` 为自由文本，未登记值原样显示）；`apiError.ts` 把 `http_501` 映射为 `err.unimplemented`。
    - i18n：中英各 38 键（键数校验 724/724 对齐）。
  - **验证**：`tsc -b` + `vite build` ✅（CSS 49.06 → 49.64 kB）；`go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅（新增 `jobdetail_test.go` 11 例全 PASS；`steps_read_test.go` 带 `pg` tag，未设 `PPTS_TEST_DATABASE` 时 SKIP，符合仓库“环境依赖型测试必须 Skip”惯例）。
- **B4-M6b 将“阶段/受影响页”升为服务端持久化列 + 排序/筛选【已实施】**（口径由用户裁定：**完整闭环 = 落列 + 排序筛选 UI**，不接受“只落列、无消费者”）：
  - **口径与动机**：M6a 已用 `input_snapshot` + `job_steps` 覆盖**展示**，故 M6b 的价值只在“可由服务端**排序/筛选**”。若只加列而无人消费，就正是本仓库明令要主动扫的“后端有、前端没用”反模式（§1.1 遗留扫描口径）。因此本里程碑**同时**交付：迁移 0026 + 排序/筛选端点 + 任务列表 UI 控件。
  - **迁移 `0026_jobs_phase_scope.sql`**（唯一 schema 变更）：
    - 新增 `jobs.phase text NOT NULL DEFAULT ''`、`jobs.affected_pages jsonb NOT NULL DEFAULT '[]'::jsonb`。
    - 回填（幂等，仅填空白）：`phase` = 每个任务 `job_steps` 中 `updated_at` 最新者对应的 `step_type`（`DISTINCT ON (job_id)`）；`affected_pages` 按 `kind` 从 `input_snapshot` 取 —— `narration` → `slides[].slideId`、`script_draft` → `slideIds`，全部以 `jsonb_typeof(...) = 'array'` 守卫，非 JSON 对象则跳过（`input_snapshot ~ '^[[:space:]]*\{'`）。
    - **刻意与 Go 逐字一致**：`narration` 只取 `slides[].slideId`、**不用 `segmentIds`**（后者只决定 `Kind='segments'`）—— 否则“范围”列与“受影响页数”排序会互相矛盾。
    - 索引 `idx_jobs_phase(tenant_id, phase, created_at DESC)`、`idx_jobs_pages(tenant_id, (jsonb_array_length(affected_pages)))`。
    - **不并入核心投影**：`jobSelectColumns` 是单一共享列清单（8 处 SQL + `ppts_claim_next_job` 的 `RETURNS TABLE` 显式列举列共用），往里加列必须重建调度函数（先例：`0018_job_traceparent.sql`）→ 新列**只出现在列表查询**里（正是本里程碑的目的：排序/筛选所需的查询列本不必进核心投影）。
    - 迁移验证：本机无 docker/psql/pg_ctl/initdb，无法真跑 → 用 `sqlglot` Postgres 方言做**语法校验**（6/6 通过），并以全量 28 个迁移做基线对比排除误报（`0013/0016/0017/0018` 的 FAIL 系 sqlglot 不支持 `LANGUAGE sql` 属性，属解析器限制而非真错）。
  - **后端 `internal/pipeline`**：
    - 新增 `scope.go`：`ScopeOf(kind, snapshot) Scope` / `AffectedPagesOf(...)` —— **快照解析的唯一实现**（自 `internal/api/jobdetail.go` 下沉；M6b 之后“落库”与“展示”必须同源）。`AffectedPages` 始终非 nil，**不设条数上限**（上限属传输层）。
    - `model.go`：新增 `JobFilter{ProjectID,Phase,Sort,Desc}` / `JobPageRow{Job,Phase,PageCount}` / `ErrBadJobCursor`；**删除 `JobStepSummary{Phase,Total,Counts,LastAt}`**（阶段改由 `jobs.phase` 承担；`LastAt` 全仓无消费者，属死字段）。
    - `postgres.go`：`Create` 写入 `affected_pages`（`mustJSONArray(AffectedPagesOf(...))`，`$8::jsonb`）；**`MarkStep` 在与步骤写入同一事务内**维护 `jobs.phase`（`UPDATE jobs SET phase=$3 WHERE id=$1 AND tenant_id=$2 AND phase <> $3`）；扫描侧抽 `jobScanBuf.dest()/job()` 供 `scanJob` 与 `scanJobPageRow` 复用，避免维护两份 17 列顺序。
    - **`MarkStep` 刻意不更新 `jobs.updated_at`**：该列供 `EventsSince` 增量轮询，写阶段若连带改它会污染轮询游标。
    - 新增 `ListPage(ctx, tenantID, f, cursor, pageSize) ([]JobPageRow, string, error)`：排序键**白名单** `created|updated|phase|pages` → `created_at`/`updated_at`/`phase`/`jsonb_array_length(affected_pages)`，未知值退回 `created_at`（防注入）；keyset 游标 `base64(值 \x1f id)`，行值比较含显式类型转换（`::timestamptz`/`::text`/`::int`/`::uuid`），`pageSize+1` 探测 next。另新增 `PhaseCounts`。
  - **端点 `GET /jobs/page`**（`internal/api/joblist.go`，挂在 `server.go` 的 `registerJobDetailRoutes` 之后）：
    - 参数 `phase`（筛）/`sort`/`dir`/`limit`/`cursor`；非法 `sort`/`dir`/`limit` → **400**；store 未实现 `JobPager` → **501**（A26）；非法游标 → **400**；其余 → 500。
    - 每行 `{jobId, projectId, kind, state, attempt, progressPercent, createdAtUnix, updatedAtUnix, phase, inputSnapshot}`；另带 `phaseCounts`（读取失败只置 `phaseCountsError:"load_failed"`，**不拖垮列表**）。
    - 上限 `maxJobPageSize=100` / 默认 20；沿用 M6a 口径**不透 `result_ref` 内部对象键**。
    - **不加角色门禁**（与 `JobService.Get/List` 同级，避免 viewer“能进列表、点开必 403”的假能力 A22）。
  - **前端**：
    - `web/src/api.ts`：新增 `JobListSort`/`JobListRow`/`JobsPageResult`/`getJobsPage`；`JobExtras` **收窄为 `{scope}`**（`stepTotal`/`stepCounts`/`stepsError` 前端从未消费，随契约一并删除，消除 M6a 遗留的双份阶段推导）。
    - `web/src/pages/Jobs.tsx`：数据源改 `getJobsPage`；新增 `.jobs-filters` 控件（阶段下拉**带计数**、排序下拉、升/降序切换），条件**写入 URL**（刷新/分享后视图一致，对齐设计方案 §206）；空态区分「无任务」与「筛选后无结果」；`applyJobUpdate` 改为**展开合并** `{...jobs[idx], ...updated}`（流式消息来自 proto `Job`、**不含 phase**，整行替换会清空阶段列）。
    - **顺带修掉一个既有真缺陷**：`job.state.toLowerCase()` 得到 `'job_state_queued'`，而 CSS 变体是 `.state-tag.queued` → **状态标签配色从未生效**；新增 `stateClass()` 统一转换（`grep job_state styles.css theme.css` 无任何结果可证）。
    - `jobs.extrasLoadFailed` 措辞改为「范围读取失败：{msg}」；中英各 **737** 键（双向 parity 校验通过）。
    - 样式：`styles.css` 新增 `.jobs-filters`/`.filter-field`/`.filter-toggle`（紧随 `.pagination` 分组），`theme.css` 补浅色覆盖。
  - **测试**：`internal/pipeline/scope_test.go`（**无 DB** 纯函数套件：`ScopeOf` 13 子用例含「narration 页列表只看 slides、不含 segmentIds」「slides 非数组退回 unknown（宁可不识别也不猜页数）」、250 页不被传输层截断、游标往返/拒垃圾、排序键映射不回声输入）；`steps_read_test.go`（`//go:build pg`）新增 `TestMarkStepMaintainsJobsPhase`/`TestCreateWritesAffectedPages`/`TestListPageFiltersSortsAndPaginates`/`TestPhaseCountsGroupsByPhase`；`internal/api/joblist_test.go` 新增 8 例（参数校验 400、默认/asc 方向透传、payload 形态、计数失败保留列表、非法游标 400、存储故障 500、未实现 501）；`jobdetail_test.go` 重写（`TestScopeFor*` 截断语义 + `publicJobsSummary` 契约收窄断言「顶层只应有 `jobs` 一个键」）。
    - PG 相关用例按仓库惯例 `t.Skip`（未设 `PPTS_TEST_DATABASE` 时输出 skip 而非 FAIL）。
  - **验证**：`tsc -b` ✅ + `vite build` ✅（55 modules，CSS 49.06 → 50.01 kB）；`go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅（**0 FAIL**；全量 14 项 SKIP 仍全为环境依赖型，`internal/api` 0 SKIP）。
- **C-6（已定案：不做）**：切租户本轮**不做**（与 §3 决策行、§6 表一致）。**2026-09-16 按文件核实**（R-8）：后端 `proto/ppts/v1/tenant.proto` 的 `TenantService` 13 个方法中**无任何租户列表 RPC**（`Members`/`Roles`/`SetMemberRole`/`RemoveMember`/`ExportTenant`/`PurgeTenant`/`Quota`/`Usage`/`ProjectUsage`/`StorageUsage`/`Policy`/`ListAuditEvents`/`ListAuditArchives`），前端 `web/src/` 亦**无切租户入口**（`tenantId` 仅出现在开发身份表单 `Login.tsx`/`auth.ts` 与只读展示 `AppShell.tsx`）。故**不存在"假能力"或"死入口"**，无需隐藏动作；本节此前"决策待定"的表述已作废。

### 零散遗留收口（2026-09-16，非批次里程碑）

按"先执行 B4-M6b、再执行零散遗留"的指令，逐项**按文件核实**后收口（R-8：清单会过时）。

| 项 | 核实结论 | 处置 |
|---|---|---|
| D0-8 favicon | `web/` 下**无任何 favicon 资源**（无 `public/`、无 `.ico/.svg`），`index.html` 也无 `<link rel="icon">` | **已补**：新增 `web/public/favicon.svg`（深色底 + 青色播放键/屏幕语汇，与 `#10131a`/`#67e8f9` 一致）+ `index.html` 挂 `link`；`vite build` 后 `dist/favicon.svg` 存在（382 B） |
| `theme-color` 不随主题 | 原值是硬编码 `#10131a`，浅色主题下浏览器 UI 与页面底色不一致 | **已补**：内联主题脚本按实际主题改写 `theme-color`（浅色 `#eef1f6`） |
| `0001_init.sql` 的 `step_type` 注释 | 注释写 `render/tts_segment/alignment/assembly/export`，与 worker **实际写入**不符 | **已修**：改为 `pages/tts_segment/timeline/export` 并注明来源文件（`app/ingest.go:123`、`app/narration.go:331,475`、`app/export.go:64`）。无校验和机制，改注释不影响已迁移库 |
| MP4 字幕烧录 | `ExportSnapshot.BurnSubtitles` 被 UI 收集、被 api 存入快照，但 `renderMP4` **完全忽略**；`MP4EncodeOptions` 无字幕项。**注意：UI 文案此前已如实标注"服务端暂不执行烧录"，故不构成 A26 假能力，而是"已披露且设计 §338 要求"的功能缺口** | **已接入**（见下） |
| artifact 缺 `duration` | `Artifact`/`NewArtifact` 均无时长字段（设计 §332 要求成品展示"实际时长"），成品的时长列一直无法展示 | **已补**：迁移 0027 + 全链路（见下） |
| R-12 `StorageUsage` 无统计时间 | 需改 proto（本环境 protoc 不可用） | **环境阻塞**（本环境 `protoc` 不可用，须在具备 `protoc` 的环境补 `tenant.proto` 增列 + 同步 `GetStorageUsageResponse`），维持"取数于 {时刻}"的如实标注 |
| C-6 切租户 | 需后端新增"我的租户列表"RPC（proto）或明确"不做" | **已定案：不做**（2026-09-16 按文件核实：后端 `TenantService` 无租户列表 RPC、前端无切租户入口，不存在假能力/死入口，无需隐藏动作；详见上「C-6（已定案：不做）」） |

**MP4 字幕烧录（已接入）**：
- **可测性重构**：`media.Encode` 里的 ffmpeg 参数构造抽成**纯函数** `compileEncodeArgs(opts, encodeInputs)` —— 不 IO、不 exec，因此**在没有 ffmpeg 的机器上也能单测滤镜链**（此前整条链只有被 Skip 的端到端用例覆盖）。新增 `internal/media/mp4_args_test.go` 11 例（含逐字符锁定的 filter_complex 断言）。
- **接线**：字幕挂为**最后一级**滤镜 —— 变时长路径 `concat=...[vbase];[vbase]subtitles=...[vout]`（杜绝"烧录后仍映射旧标签"的静默失效），等时长路径追加到 `-vf`。
- **路径转义**：字幕以**固定 basename**（`subtitles.srt`）引用 + `exec.Cmd.Dir = 工作目录`，规避 filtergraph 里 Windows 盘符冒号/反斜杠的转义坑。
- **能力探测 + A26**：新增 `MP4Encoder.SupportsSubtitles`（`ffmpeg -filters` 是否含 `subtitles`，进程内缓存）；缺 **libass** 时返回哨兵错误 `ErrSubtitlesUnavailable`，由 `renderMP4` 转成**明确失败原因**——不静默产出一段没有字幕的视频。
- **同源**：烧录用的 SRT 直接取自**同一条时间轴产物**（`bundle.SRTKey`），保证画面字幕与可下载的 `.srt` 一致；抽成 `subtitleForBurning` 以便无 ffmpeg 单测（新增 2 例：同源 + SRT 缺失必须失败）。
- **UI 文案**：`editor.exportBurnNote` 从"暂不执行烧录"改为**真实行为**（含"服务端 ffmpeg 缺 libass 会明确失败"）；顺带为 `includeNotes` 补 `exportIncludeNotesNote` —— 核实发现该开关**只影响快照标识**（会多出一个内容相同的成品版本），不写进任何产物，按 A26 明说而非留白。
- **部署前提（未在本机验证）**：烧录依赖 ffmpeg 带 libass，且 **libass 渲染中文需要 fontconfig 下有 CJK 字体**；本机无 ffmpeg，故端到端路径仅能靠上述单测 + 语法锁定保障，**需在具备 ffmpeg+libass+中文字体的环境做一次实机验收**（已登记为待验项）。

**artifact 时长（已补）**：
- 迁移 `0027_artifact_duration.sql`：`artifacts.duration_ms bigint NOT NULL DEFAULT 0`（**0 = 未知/未记录**，界面显示「—」不伪造；历史行保持默认值）。仅加列，表级授权已覆盖，无需新 GRANT/RLS。
- 写入：`app.ExportHandler` 用 `bundle.Timeline.DurationUS / 1000` 落库 —— MP4 画面长度、SRT/VTT 字幕跨度、Web 工程总长**同源**，四种格式含义一致，无需按格式分支。
- `internal/artifact/postgres.go`：抽出**单一列清单** `artifactColumns`（Create 的 RETURNING / Get / ListByProject / scan 共用），避免新增列时只改一处导致扫描错位（该错位编译期不可见）；`scan`/`scanRows` 收敛为同一实现。冲突分支顺带 `duration_ms=EXCLUDED.duration_ms`，让 0027 之前的历史行在重导出时自愈。
- 端点 `GET /projects/{pid}/artifacts` 增加 `durationMs`；前端 `ProjectArtifacts.tsx` 增加「实际时长」列（`分:秒`，0 显示 `—`）。
- 测试：`app/export_test.go` 的桩补 `DurationMS` 透传，并在无 ffmpeg 也会执行的 SRT 用例中断言"时长 == 时间轴时长且 > 0"。

**验证**：`gofmt` 清白；`go build ./...` + `go vet ./internal/...` + `go test ./internal/...` ✅（22 包全绿、**0 FAIL**）；`tsc -b` ✅ + `vite build` ✅（CSS 50.01 kB、`dist/favicon.svg` 就位）；迁移以 `sqlglot` Postgres 方言校验（0027 单语句 OK）。

### B5 可选增强（独立立项，2026-09-16 范围裁定）

**范围裁定（B5-C1）**：本轮做 **3 块**——命令面板、全局成品库、**用户作品公开发布与撤回（A29）**；**邮箱自助注册延后到下一轮单独立项**（自注册用户如何归属租户尚未设计、风险最高，C-8 原计划 4 块中的该块移出本轮）。

**CDN 清理决策（B5-C2）**：A29 要求「撤回即失效含 CDN 清理」，但 `objectstore` 接口无 `Invalidate/Purge`（local/s3 仅 Put/Get/SignedURL/Delete/ApplyLifecycle）。裁定：**撤回 = DB 置 `withdrawn` 状态立即拒匿名读 + 公开资源走现状 1h TTL 签名 URL 自然失效**；环境无 CDN 故「CDN 清理」项豁免并注明。满足「立即失效」，不满足字面 CDN purge（环境不可达）。

**约束**：protoc 不可用 → 全部走原生 HTTP（`mux.Handle`/`writeJSON`/`requireRole`/`tenant.Run` 样板）；强多租户 RLS 双策略；新增后端路由须核对注册表（R-10）。

**里程碑拆分（独立验收，每里程碑一 commit）**：

| 里程碑 | 内容 | 状态 |
|---|---|---|
| B5-M1 命令面板（Cmd/Ctrl+K） | 纯前端：AppShell 全局 Cmd/Ctrl+K 监听 + 顶栏 ⌘K 按钮；`CommandPalette` 复用 `useDialogA11y`（Esc/焦点陷阱/焦点返回），命令由 `primaryNav`/`settingsMenus` 按角色过滤（导航+设置子页）+「新建讲解」/「公开区」操作；方向键选择、回车执行、输入过滤；i18n 中英、样式入 styles.css | 【已实施】 |
| B5-M2 全局成品库 | 后端 `GET /artifacts`（`artifact.Store.ListAll` 跨项目 JOIN projects 取项目名，`tenant.Run` 多租户；`requireRole(RoleOwner)` 门控，高于单项目 `artifact.list` 的 editor）+ 前端 `/library` 页（`library.view`=ROLE_OWNER 能力；侧栏入口带 `need` 过滤保持菜单与后端一致；按格式/项目/时间筛选、下载、跳转项目成品页）；i18n 中英 + 样式入 styles.css。原生 HTTP 端点，不改 proto | 【已实施】 |
| B5-M3 公开发布与撤回（A29） | 迁移加 `publications.public_id`(32B 随机 base62 不可反推) + `status` 加 `withdrawn`/`withdrawn_at`；匿名读从内部 `id` 切到 `/showcase/:publicId`；新增 `POST /public/works/:publicId/recall`（owner/admin 置 withdrawn 立即拒读）；删除级联失效（artifact 删→publication 连带 withdrawn）；验收 A29。**门控**：能力不完整则入口整体不上线（C-8） | 待实施 |

**验证回路**：`contrast.mjs` 0 失败 → `tsc -b` → `vite build`；后端 `go build ./...` + `go vet ./internal/...` + `go test ./internal/...`；每里程碑一 commit 一 push；匿名发布/撤回须独立最小字段集 + 越权测试（R-15/R-2）。

**出口**：B5-M1/M2/M3 三项验收通过；发布入口能力完整才上线，否则整体隐藏（C-8 门控）。

### 视觉基线专项（建议与 B1 并行，独立验收）

| 项 | 内容 |
|---|---|
| 背景 | 现实现为深色主题，V1.6 §14 要求浅灰工作区 + 白色面板 + 单一蓝紫主色；两者不可"各改一半" |
| 任务 | ① 建立设计 token（色板/字号/间距/圆角/密度）并统一落 `styles.css` 或拆分样式；② 圆角从 999px 收敛为 6~8px 控件 / 10~12px 卡片；③ 焦点可见、弹窗 focus trap 与 Esc、关闭后焦点返回；④ 状态改为图标+文字+颜色；⑤ 空格仅非输入焦点时控制播放；⑥ 对比度 4.5:1 校验；⑦ 触控 44px |
| 出口 | 视觉与可访问性基线通过评审；不因改视觉而重构技术栈 |

**决策（已定）**：保留深色为默认 + 新增浅色切换（见 C-3）。本专项聚焦"主题令牌体系 + 切换按钮 + 浅色令牌集"：先以 `theme.css` 的 `html[data-theme="light"]` 覆盖集落地浅色，后续再统一收敛为单令牌系统；不再做深→浅整体重构。

**里程碑拆分（独立验收，每个里程碑一个 commit）**：

| 里程碑 | 内容 | 状态 |
|---|---|---|
| V-M1 设计令牌体系 | `styles.css :root` 定义深色令牌集、`theme.css` 同令牌名浅色重定义；`styles.css` 颜色/渐变/阴影字面量按 `(选择器,属性)→浅色覆盖` 驱动替换为 `var(--token)`（仅令牌化、主题不变更、深色/浅色视觉等价） | 【已实施】 |
| V-M2 对比度门禁 | 新增零依赖 `contrast.mjs` 算 WCAG 比值并接入 `npm run build` 前置（`build: "npm run contrast && tsc -b && vite build"`）；修正不足文本令牌：浅色 `--text-dim`/`--text-faint`/`--text-muted`/`--text-faintest` 收敛到 `#5b6b7e`（对白/浅底 ≥4.5:1）；深色 `--text-dim`/`--text-faintest` 提到 `#7e8aa3`（最深表面 `#151d2e` 仍 ≥4.8:1）。门禁仅校验 `--text-*` 令牌契约（确定性、无级联假阳性）；浅色模式「深色字面量文本未主题化」与「状态色 chip 浅色微调」为独立遗留，不在阻断范围 | 【已实施】 |
| V-M3 焦点可见 + 弹窗可访问性 | 全站 `:focus-visible` 焦点环（青色 `var(--accent)` + 2px offset）+ `:focus:not(:focus-visible)` 抑制鼠标默认 outline；移除 2 处 `outline:none`（`.editor-card`/`.segment-card` textarea，改由 `:focus-visible` 提供键盘焦点环，原有 `:focus` box-shadow 保留）；新增 `web/src/a11y.ts` 的 `useDialogA11y`（打开移焦入内、Tab 循环焦点陷阱、Esc 关闭、关闭后焦点返回触发元素），接线 7 处 `role="dialog"`（AppShell 用户菜单 / GatewaySettings / ExportDialog / ImportDialog / ScriptEditor 读音 popover / ProjectEditor 属性抽屉 / ProjectEditor 发布弹窗）并补 `aria-modal="true"` | 【已实施】 |
| V-M4 圆角收敛 + 状态图标三重编码 | 收敛为三档半径：`styles.css` 控件类（button/input/select/textarea 及明确按钮·触发·缩略图）8px、卡片/面板/表面 12px、徽标/标签/状态 chip/头像/点保留 999px 或 50%（全屏态 `border-radius:0` 保留）；脚本化收敛避免 60+ 处手写误差，并修正 `.project-card` 既有重复 `border-radius` 声明。`state-tag`(10 任务态 + 4 步骤态)/`.health-tag`(5) 用 CSS `--glyph` + `::before` 注入语义图标（•/▶/↻/✎/✓/✕/⊘/?/○/⚠/—），与既有颜色 + `t()` 文字构成「颜色+图标+文字」三重编码，0 TSX 改动、浅色主题只覆盖颜色不影响图标 | 【已实施】 |
| V-M5 桌面触控 44px + 专项出口验收 | `styles.css` 新增 `@media (pointer: coarse)` 块，对 button/input/select/textarea 及主要触发/导航/弹窗/播放/缩略图按钮统一触控目标 `min-height/min-width: 44px`（与 `max-width` 移动端断点解耦，覆盖桌面触屏，满足 WCAG 2.5.5）；视觉与可访问性基线（V-M1 令牌 / V-M2 对比度 / V-M3 焦点+弹窗 / V-M4 圆角+状态图标 / V-M5 触控）全部完成，§2.10 差距表已收口 | 【已实施】 |

---

## 6. 决策点（2026-09-16 全部定案）

> **状态**：C-1–C-7 均已定案（C-1/C-4/C-5 已实施；C-2/C-3/C-7 为口径决策；C-6 明确不做，已按文件核实无入口），C-8 延后至 B5 独立立项。**本表已无待定项**。

| 编号 | 决策点 | 可选路径 | 影响 |
|---|---|---|---|
| C-1 | 公开区内容来源 | **【已定 + 已实施】双 Tab**：①「官方精选」= 管理员在控制台上传并发布到精选目录（`POST /public/featured`，admin）；②「用户作品」= 用户主动发布（`POST /public/works` → `pending`），经管理员审核通过（`PUT /public/works/{id}/review` → `approved`）后公开展示。后端 `publications` 表 + RLS 双策略，公开只读接口仅读取 `approved` 集（`63ede93`/`cc506c7`/`279d820`/本轮管理前端）。音频播放（Watch 页）按设计延后至 B3 | B1 后端扩为：精选目录 CRUD + 用户发布/审核流；公开列表/详情只读 approved |
| C-2 | SEO 策略 | **【已定】A（元数据 + CSR）**：先落地 title/description/OG + 干净 URL（history 路由），预渲染/SSR 延后，待 SEO 需求明确再评估 | 本轮不引入预渲染与分离构建；公开区可发现性以元数据满足 A27 |
| C-3 | 主题基线 | **【已定】保留深色为默认，新增浅色切换**：顶栏右上角太阳/月亮按钮切换深/浅；浅色以浅灰工作区 + 白色面板 + 蓝紫主色为基调（非全量改 §14） | 视觉基线专项改为"主题令牌 + 切换"，不再做深→浅整体重构 |
| C-4 | 路由形态 | **【已定 + 已实施】A（history + 服务端 fallback）**：自研 hash 路由已改为 History API（`b96352b`）；dev 由 Vite SPA fallback 兜底，prod 由 Go 后端 catch-all 经 `PPTS_WEB_ROOT` 返回 index.html（`e1e6169`）；遗留 `#/path` 深链接在挂载时改写为 `/path` | 公开区可访问性与所有既有链接需重测；B1 其余项待做 |
| C-5 | 生成口径 | **【已定 + 已实施】强制阻止未确认稿正式生成（A09）**：`draftSegments>0` 时前端禁用正式生成按钮并提示，生成请求传 `lockConfirmedOnly=true`；后端 `CreateGeneration` 的 `RequireConfirmed` 校验未确认稿即返回 `FailedPrecondition` | 已落地，不再待定 |
| C-6 | 切租户 | **【已定：不做】**本轮明确不做切租户；**2026-09-16 按文件核实**：后端 `TenantService` 无租户列表 RPC、前端无切租户入口，不存在假能力/死入口，无需隐藏动作 | 无需新增接口；本项关闭 |
| C-7 | 个人设置 | **【已定】明确不做并移除入口**：遵守 §1.2 第 3 条；若设置菜单已有个人设置入口则隐藏，本轮不实现个人设置页（留作后续 B4） | B4 ④ 顺延；当前不暴露虚假能力 |
| C-8 | 公开作品发布 | **【已定 + 已立项 2026-09-16】**归入 B5 可选增强，范围裁定为 3 块（命令面板+全局成品库+公开发布 A29），邮箱注册延后；CDN 清理按 B5-C2 状态机即时失效+TTL 豁免；发布能力不完整则入口整体不上线（B5 门控） | B5-M3 实施中；A29 随 B5-M3 验收 |

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
| R-11 | ~~**D0 批次严重滞后**~~ **【已解决 2026-09-16】**：D0 原定"B1 之前或并行完成"，实际拖到 B4-M5 时仍有 3 项未做，其中 **D0-2 为安全项**（生产构建未配 OIDC 时仍渲染开发身份表单，违反 A01/A02） | 已发生 | 高 | **D0 九项全部收口、批次归零**：D0-1/5/6/8 早前完成、D0-3/D0-4 由 B4-M5 修复、**D0-2 + D0-7 + D0-9 由 2026-09-16「D0 收口」完成**（D0-2 含产物级双向验证 `return!1`/`return!0`；D0-9 使 CSS 51.44→49.06 kB）。教训：**缺失项要按文件核实、不要沿用清单描述**（D0-7 的清单描述与仓库实际不符），并坚持每批次出口逐条勾选（R-8） |
| R-12 | `TenantService.StorageUsage` 未返回统计时间字段，首页无法满足 V1_6 §202「存储必须标统计时间」 | 已发生 | 低 | 首页如实标注为"取数于 {时刻}"（不伪造服务端统计时间）；如需真实统计时间，须后端在 `tenant.StorageUsage` 增列并同步 `GetStorageUsageResponse`（proto 变更，本环境 protoc 不可用 → 需在具备 protoc 的环境补） |
| R-13 | ~~**提交在途期间的编辑被静默丢弃**~~ **【已解决 2026-09-16】**：`ScriptEditor` 处于 `saving` 时用户继续输入，`runCommit` 的 `.then` 调 `onChange(saved)` 推进 `revision` → 复位 effect 用服务端文本覆盖 `texts`（并把 `saveState` 置回 `saved`），用户新输入既无"未保存"提示也无恢复入口；同源缺陷还包括**在途期间以同一 `expectedRevision` 并发提交**（必判 conflict） | 已发生 | 高 | D0-7 收口时发现、**先于本轮存在**，2026-09-16 单独收口。修法：复位 effect 用 `slideIdRef` 区分"切页"（必须重置）与"revision 推进"（保留本地未提交文本）；在途判定改用按 slideId 记名的 `inFlightRef`（不再复用会被新编辑置回 `dirty` 的 `saveState`），在途时 `scheduleSave`/`flush` 一律退避重排；提交返回用 `editSeqRef` 识别"在途期间又有新编辑"，不置回 `saved` 而是立即重排。`error` 保留对齐分支以兼容冲突对话框。**第二轮收口 2026-09-16**：第一轮修法只覆盖"同一页内"的三条不变式 —— 在「提交在途 + 用户切页」这条同源路径上仍会**静默丢失原页编辑**（切页的 `clearTimeout` 清掉了为原页排的退避重排，且 `texts` 被新页文本覆盖，编辑既未提交也未回到父级）。已按"页"重构保存机（草案表 + 按页在途/定时器 + 可提交任意页的 commit 通道 + 冲突哨兵），详见 B4 章节「R-13 修复」 |
| R-14 | **把"文档已登记已解决"当成"代码已无同类残留"**：R-13 第一轮修法未推演同源路径（切页）即被标【已解决】，残留漏洞会以"已修项"身份长期潜伏（同类教训见 R-8/R-10） | 中 | 高 | 修复结论须按「不变式 + 全路径推演」给出，并在风险表区分"第一轮/第二轮"；凡涉及跨页/跨组件状态，必须逐一列出该状态在**切页 / 回包 / 冲突 / 卸载**四条时序下的行为，再判定是否已解决 |
| R-15 | **匿名公开接口越权（R-2 同源）**：B5-M3 发布/撤回在公开区新增写/状态变更，若复用租户上下文接口会把内部 `id`/租户字段回带，导致字段越权泄露或撤回绕过 | 中 | 高 | 强制独立最小字段集 + 独立路由（`POST /public/works/:publicId/recall` 仅持 `public_id`+principal，不回带租户内部 `id`）；对匿名读/撤回单独写越权测试；`withdrawn` 状态即拒匿名读且不清内部资源 |

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
