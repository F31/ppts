# PPTS 控制台优化方案实施核对与改进建议

> 版本：V1.0｜日期：2026-09-19
> 输入：《控制台产品优化实施计划-V1.0》（workbuddy 任务清单）＋《前端功能与交互体验优化方案》＋实际代码核对
> 方法：**按文件核实**（R-8/R-14 教训）——不信任文档「已实施」标记，逐项落到代码/迁移/路由/前端调用点取证。

---

## 一、核心结论（TL;DR）

| 优化方案缺口 | 实际代码状态 | 判定 |
|---|---|---|
| **§1.1 多版本管理** | 已做「只读查看 + 预览抽屉」（P0，commit `de60719`） | ⚠️ **部分完成**——缺切换生效版本/备注/删除/差异 |
| **§1.2 分类打标签** | 已完整落地（P1，commit `7f2ff66`，迁移 `0031`，含 RLS） | ✅ **已完成**——方案此项已过时 |
| **§1.3 指定用户分享** | 后端/前端/迁移**均无任何实现** | ❌ **完全未做**——最大真实缺口 |

> **关键发现**：优化方案基于的基线晚于实际代码进度。方案 §1.2（标签/分组）与 §2.1（项目列表搜索/筛选/URL）**已经被代码实现并超越**；方案 §1.1（版本）只做了一半；方案 §1.3（指定用户分享）才是真正未触及的空白。

---

## 二、workbuddy 任务清单 vs 实际代码核对

workbuddy 的清单载体是 `docs/PPT智能语音讲解平台-控制台产品优化实施计划-V1.0.md`（R-1~R-16、A01~A29、D0、B0~B5、C-1~C-8 决策）。**对照实际代码逐项取证：**

### 2.1 已落地并被代码超越（文档滞后）

| 清单项 | 文档标记 | 代码取证 | 结论 |
|---|---|---|---|
| B4-M6b 阶段/受影响页落列 | 【已实施】 | 迁移 `0026` + `internal/api/joblist.go` + `Jobs.tsx` URL 筛选 | ✅ 已实现 |
| B5-M2 全局成品库 | 【已实施】 | `GET /artifacts` 路由 + `/library` 页 | ✅ 已实现 |
| B5-M3 公开发布/撤回 | 【已实施】 | 迁移 `0028` + `/showcase` + `/public/works/*` | ✅ 已实现 |
| B5-M4 邮箱自助注册 | 【已实施】 | 迁移 `0029` + `/auth/register` + `Login.tsx` 邮箱 tab | ✅ 已实现 |
| R-13 提交在途丢失修复 | 【已解决】 | `web/src/ScriptEditor.tsx` 草案表 + 按页保存机 | ✅ 已实现（workbuddy 记忆唯一条目，2026-09-16） |
| V-M1~V-M5 视觉基线 | 【已实施】 | `styles.css`/`theme.css`/`contrast.mjs`/`a11y.ts` | ✅ 已实现 |
| **#94 标签+分组** | **（清单未登记，方案当作缺口）** | 迁移 `0031` + `internal/api/tagfolder.go` + `Projects.tsx` | ✅ **已实现，且优于方案设计** |
| **#93 项目列表搜索/排序/URL** | **（清单未登记，方案当作缺口）** | commit `69b9384`，卡片/表格双视图 | ✅ **已实现** |
| **#多版本查看（P0）** | **（清单未登记，方案当作缺口）** | commit `de60719` + `GET /projects/{pid}/revisions` | ⚠️ **只读查看，未达方案全文** |

### 2.2 标记与代码不符（需修正的文档）

| 清单项 | 文档标记 | 代码取证 | 问题 |
|---|---|---|---|
| `CreateSourceRevision` | README 称已实现 | `internal/api/project.go:181` | 实际 `return Unimplemented`（内部归 UploadService）——文档若引用会误导 |

### 2.3 风险登记中仍开放但未列出的项

R-12（`StorageUsage` 无统计时间字段）仍标记【环境阻塞 protoc】，但代码已走原生 HTTP 绕过 proto，**该阻塞理由已失效**——`/jobs/*`、`/tags`、`/revisions` 均为原生 HTTP 成功落地的先例，R-12 可用同法收口。

---

## 三、三缺口逐一核对（核心）

### 3.1 多版本管理 —— ⚠️ 部分完成

**已具备：**
- 后端 `GET /projects/{pid}/revisions`（`internal/api/editor.go:176`）返回 `current_revision` + 版本倒序列表，字段含 `revision_no / created_at / page_count / parser_version / object_key / is_current`。
- 前端 `ProjectEditor.tsx` 版本抽屉（只读预览，`versionsOpen` 状态）。
- `source_revisions` 表本身支持不可变版本 + 软删。

**缺失（方案 §1.1 期望但代码没有）：**

| 能力 | 现状 | 缺口 |
|---|---|---|
| 切换生效版本「设为当前」 | ❌ 无 `SetCurrentRevision` | 前端只读，无法切回旧版 |
| 版本备注/命名 | ❌ `source_revisions` 无 `note` 列 | 无法「客户评审版」式命名 |
| 删除非当前版本 | ⚠️ 有软删字段，无 UI/接口 | 无 `DELETE` 端点 |
| 版本差异（diff） | ❌ 无 | 页级 diff 未实现 |
| 切换后的讲稿覆盖提示 | ❌ 无 | 需在切版本时明确提示讲稿按 `revision_id` 关联、不会自动迁移 |

### 3.2 分类打标签 —— ✅ 已完成（方案已过时）

**代码证据（优于方案设计）：**
- 迁移 `0031_tags_folders.sql`：`tags`（租户内 name 唯一 + color）、`folders`（单归属）、`project_tags`（多对多 + tenant_id），`projects.folder_id` 可空外键（删分组 SET NULL 回落未分类），**全部 FORCE RLS + 索引 + 权限 GRANT**。
- 后端 `internal/api/tagfolder.go`：完整 CRUD——`GET/POST /tags`、`PUT/DELETE /tags/{id}`、`GET/POST /folders`、`PUT/DELETE /folders/{id}`、`PUT/DELETE /projects/{pid}/tags/{tagId}`、`PUT /projects/{pid}/folder`、`GET /projects/organization`。
- 前端 `Projects.tsx` + `api.ts`：标签/分组多选筛选、URL 参数化（可分享/刷新保留）、批量打标签/移动分组、卡片/表格双视图。
- 方案 §1.2 建议的 proto `TagService`/`FolderService` **无必要**——原生 HTTP 已实现且门禁一致。

**仍可增强（非缺口，锦上添花）：**
- 标签管理页（重命名/合并/删除的集中管理 UI，目前散在列表交互）。
- 批量归档（方案 §1.2 提到，确认是否存在 UI）。

### 3.3 指定用户分享 —— ❌ 完全未做（最大缺口）

**确认无任何实现：**
- 迁移：无 `project_collaborators` / `project_share_links` 表。
- 后端：无 `CollaborationService`、无 invite/role/remove 路由、无 share-link 路由。
- 前端：`ProjectEditor.tsx` / 项目卡片无「协作者」入口。

这是**企业协作刚需**且与「公开发布」完全不同的能力，方案 §1.3 需整体落地。

---

## 四、完整度评估

### 4.1 方案三缺口整体覆盖率

```
§1.1 多版本    ██████░░░░  60%  （只读查看完成，切换/备注/删除/差异未做）
§1.2 标签分组   ██████████  100% （已完成，超出方案）
§1.3 指定用户分享 ░░░░░░░░░░   0%  （完全未做）
```

### 4.2 方案其余体验项现状

| 方案项 | 现状 |
|---|---|
| §2.1 项目列表卡片/表格 + URL | ✅ commit `69b9384` 已做 |
| §2.3 导入进度可视化 | ⚠️ 分片上传 API 就绪，取消按钮未接（`AbortUpload` 无 UI） |
| §2.4 讲稿确认/锁定三态 UI | ✅ M3 已实现（`approve`/`lock`），ScriptEditor 五态上报 |
| §2.4 真实播放器 | ⚠️ 方案标 P2；`Player.tsx` 用 `performance.now()` 模拟时钟（playback 真实音频已可播，时间轴对齐待真人试听验收 R-5） |
| §2.5 成品按格式分组 | ⚠️ `ProjectArtifacts.tsx` 有分组，导出前「讲稿已改但音频未更新」提示待确认 |

---

## 五、改进项目清单（按优先级）

### P0 — 快速见效（后端基建已有或工作量小）

| # | 改进项 | 说明 | 落点 |
|---|---|---|---|
| 1 | **版本切换「设为当前版本」** | 补 `PUT /projects/{pid}/current-revision` + 前端按钮 + 切换前讲稿覆盖范围提示 | `internal/api/editor.go` + `ProjectEditor.tsx` |
| 2 | **导入取消 + 真实进度** | 接 `AbortUpload` 取消按钮；分片逐片汇总真实百分比 | `ImportDialog.tsx` |
| 3 | **R-12 收口** | 用原生 HTTP 绕过 protoc 限制，给 `StorageUsage` 加统计时间字段 | `internal/api/` + `Home.tsx` |

### P1 — 核心新增（对应方案 §1.3，工作量大）

| # | 改进项 | 说明 | 落点 |
|---|---|---|---|
| 4 | **邀请协作者（私密分享前半）** | 新表 `project_collaborators`（复用 5 级角色）+ `CollaborationService` 增删改查 + 项目卡片协作者头像堆叠 + 权限即时生效（403 越权测试） | 迁移 + `internal/api/collab.go` + 前端 |
| 5 | **协作者权限收回** | 被移除者旧会话下次请求返回明确「重新申请权限」，非无限跳登录 | `auth.go` + 前端 session |

### P2 — 增强（方案 §2.x，可分批）

| # | 改进项 | 说明 |
|---|---|---|
| 6 | **链接分享（带密码/有效期）** | `project_share_links` + 独立最小字段匿名路由（沿用 R-2/R-15 越权原则），放在协作者之后 |
| 7 | **版本备注 + 删除** | `source_revisions` 加 `note`；`DELETE` 非当前版本（保底保留 1 个） |
| 8 | **版本 diff** | 页级对比（页数/标题变化提示） |
| 9 | **标签集中管理页** | 标签重命名/合并/删除 + 影响说明 |
| 10 | **导出前音频陈旧提示** | 「讲稿已改但音频未更新」二选一，不静默混用 |

---

## 六、对 workbuddy 任务清单本身的改进建议

1. **登记清单滞后项**：把「#93 项目列表」「#94 标签分组」「#多版本查看」补进实施计划对应章节状态（当前文档把这三个当作「未做缺口」，已过时）。
2. **修正 `CreateSourceRevision` 描述**：`project.go:181` 实为 Unimplemented（内部归 UploadService），README/文档若引用需更正。
3. **R-12 阻塞理由作废**：protoc 不可用已被「原生 HTTP 绕过」全面突破，应删除该阻塞标记并落地收口。
4. **新增 R-17（版本切换数据一致性）**：切换生效版本可能让「当前版本下的讲稿/配音」失配，需按 R-14 的「切页/回包/冲突/卸载」时序做全路径推演，避免重蹈 R-13 覆盖缺口覆辙。
5. **新增 R-18（协作者越权）**：协作分享引入项目级访客 + 权限收回，须沿 R-2/R-15 原则单独写越权测试，且不要复用租户上下文接口回带内部 ID。

---

## 附：核对取证索引

| 证据 | 位置 |
|---|---|
| 标签/分组后端路由 | `internal/api/tagfolder.go:14-46`、`server.go:141` |
| 标签/分组迁移 + RLS | `migrations/0031_tags_folders.sql` |
| 标签/分组前端 | `web/src/api.ts:987-1050`、`web/src/pages/Projects.tsx:44-93` |
| 版本查看端点 | `internal/api/editor.go:174-215`、`server.go:139` |
| 版本查看前端 | `web/src/api.ts:299-314`、`web/src/pages/ProjectEditor.tsx:98-101` |
| 协作分享缺失证据 | 迁移无 collaborator/share 表、`internal/api` 无 collab 路由、前端无分享入口 |
| 相关提交 | `de60719`(版本) `69b9384`(列表) `7f2ff66`(标签) `ebec20c`(成员) |
| R-13 修复 | `.workbuddy/memory/2026-09-16.md`、`docs/...实施计划-V1.0.md` §7 R-13/R-14 |