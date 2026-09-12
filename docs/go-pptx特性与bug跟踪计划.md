# go-pptx 特性与 bug 跟踪计划

> 版本：V1.0｜编制日期：2026-09-12
> 适用：ppts（`/mnt/e/projects/ppts`）开发过程中对 go-pptx（`/mnt/e/projects/go-pptx`）的能力增强与缺陷修复。
> 执行方：code-buddy 在本文件驱动下，于 go-pptx 仓库执行；ppts 开发只做提出、验收与集成。
> 依赖基线：go-pptx v1.0.0（2026-09-11 发布）；API 分级按 ADR-015。

## 1. 目的与边界

- ppts 开发中任何对 go-pptx 的**特性增强**或**缺陷修复**，一律登记为本文件条目，交由 code-buddy 在 go-pptx 仓库执行；**ppts 侧不直接修改 go-pptx 代码**。
- go-pptx 以正式模块 `github.com/F31/go-pptx` 依赖：开发期经 `go.work` 指向本地产库，发布锁定正式 tag。
- 本文件只解决"go-pptx 需要改什么"，不重述 ppts 业务方案（见《PPT自动讲解工具-技术方案-V3.6.md》与《PPT自动讲解工具-开发计划-V1.0.md》）。

## 2. 角色分工

| 角色 | 职责 |
|---|---|
| ppts 开发（提出者/验收者） | 登记条目、定义验收标准、在 ppts 侧跑适配器契约测试、标记 VERIFIED/CLOSED、仲裁状态 |
| code-buddy（执行者） | 在 go-pptx 仓库实现条目、修复缺陷、跑质量门禁、更新 go-pptx 文档（CHANGELOG/实施状态跟踪/能力矩阵/必要 ADR）、提交 PR 并标记 RESOLVED |

边界：code-buddy 只改 go-pptx 仓库；ppts 仓库的任何改动由 ppts 开发负责。条目验收的最终判定归 ppts 开发。

## 3. 状态机

```
NEW ──评审──> IN_PROGRESS ──合并──> RESOLVED ──ppts集成验收──> VERIFIED ──> CLOSED
 │               │                          │
 ├─> NEEDS_INFO  │                          └─> REOPENED（验收不通过）
 ├─> REJECTED    └─> BLOCKED ──> NEW（重排）/ REJECTED
```

| 状态 | 含义 | 责任方 |
|---|---|---|
| NEW | 已登记待评审 | ppts 开发 |
| IN_PROGRESS | code-buddy 正在 go-pptx 实现 | code-buddy |
| RESOLVED | 变更已合入 go-pptx，等待 ppts 集成验收 | code-buddy |
| VERIFIED | ppts 适配器契约测试通过 | ppts 开发 |
| CLOSED | 已回归入库，条目闭环 | ppts 开发 |
| REOPENED | 验收不通过，退回 IN_PROGRESS | ppts 开发 |
| NEEDS_INFO | 描述/复现/验收不完整，需补充 | ppts 开发 |
| BLOCKED | v1.0 约束或外部依赖下无法实现，返回分析 | code-buddy |
| REJECTED | 不采纳或由 ppts 侧 OOXML 补丁兜底 | ppts 开发 |

**BLOCKED 升级路径**：code-buddy 给出"为什么无法在 v1.0 约束下实现"分析 → ppts 决策：① ppts 侧 OOXML 保真补丁兜底（接口保持稳定）；② 与 go-pptx 协商新增 ADR 调整约束后重排；③ 降级为"明确不支持"并在界面/文档声明。

## 4. 条目模板

登记每个条目时完整填写以下字段：

```text
## ID-XXX 标题

- 类型：feature | bug | refactor
- 状态：NEW | IN_PROGRESS | RESOLVED | VERIFIED | CLOSED | NEEDS_INFO | BLOCKED | REJECTED
- 优先级：P0（阻塞里程碑）| P1（产品化增强）| P2（独立扩展）
- 登记日期：YYYY-MM-DD
- 提出者：ppts（关联任务：G0-8 / G1-2 / ...）
- 关联需求：指向 ppts 计划 §/任务编号与验收原文

### 描述
（需求：要做什么、为什么；缺陷：现象、影响范围）

### 复现（bug 必填）
- 最小样例：文件路径或最小构造步骤（语料编号/公开金样）
- 预期行为：…
- 实际行为：…
- 环境：go-pptx commit / 客户端版本（如 PowerPoint 16.0.20326）

### 验收标准（可测断言）
- [ ] 断言 1（行为测试，禁止"接口已定义/XML 已生成"即视为完成）
- [ ] 断言 2
- [ ] 客户端真机验证（如适用，登记客户端与版本）

### API 影响（ADR-015）
- 触达类型：Stable（只能追加，不破坏签名/字段/语义）| Experimental（可演化，注明边界）| API 默认（仅追加字段）
- 新增导出符号：…（入 go-pptx 冻结清单 §D 待评审区）

### 预估影响文件
- …

### 完成记录
- PR / commit：…
- go-pptx 文档同步：CHANGELOG / docs/go-pptx-实施状态跟踪.md / 能力矩阵 / ADR
- ppts 验收结果（由 ppts 开发填写）：…
```

## 5. 质量门禁（code-buddy 完成条目必须全绿，对齐 go-pptx DoD）

在 go-pptx 仓库内执行，缺一不可：

1. `gofmt -l .` 零输出
2. `go vet ./...`（默认 + `-tags=corpus`）零警告
3. `go test ./...` 全绿
4. `go test -tags=corpus ./...` 全绿
5. `scripts/gen_corpus/run.sh validate testdata/corpus`（36 样本 0 错误）
6. 4 交叉构建（linux CGO=0 / js-wasm / wasip1-wasm / 必要平台）零失败
7. 覆盖率不低于口径：root ≥82%、full ≥80%、低层格式包 ≥90%
8. API：不破坏 Stable 契约（ADR-015）；新导出符号登记 go-pptx 冻结清单 §D 待评审区
9. 能力矩阵档位提升：测试证据 + 必要 ADR（矩阵行 ↔ 工作包追踪制）
10. 文档同步：CHANGELOG、`docs/go-pptx-实施状态跟踪.md`"最近更新"表、能力矩阵、必要 ADR
11. 新增能力涉及新语料时：登记来源、许可、生成器版本、字体环境（`docs/corpus-入库指南.md`）
12. 无 P0/P1 缺陷残留

任一失败必须修正后重跑，不允许以"跳过"名义合入。

## 6. 验证闭环（ppts 侧，由 ppts 开发执行）

1. 条目进入 RESOLVED 后，ppts 开发将验收标准转化为适配器契约测试（`internal/project` 读路径 / `internal/integrations` 写路径）。
2. 通过 `go.work` 指向已更新的本地产库运行 ppts 测试，逐条核对验收标准。
3. 全部通过 → 标记 VERIFIED → 相关 ppts 回归用例入库 → CLOSED。
4. 任一不通过 → REOPENED，附差异与复现，退回 code-buddy。
5. 累积 N 个 VERIFIED 且 ppts 里程碑验收需要时 → 请求 go-pptx 打 release tag（如 v1.1.0），ppts `go.mod` 锁定正式 tag；CI 用 tag 拉取，不依赖本地产库。

## 7. 初始待办队列

### G0 能力对照表（2026-09-12 ppts 实测，go-pptx 本地产库 v1.0.1）

> 依据：ppts 读/写适配器契约测试（`internal/project/reader_test.go`、`internal/integrations/narrationwriter_test.go`）。
> 表内"已实测可用"指 ppts 端已通过自动化断言；"保持 NEW"指仍需 go-pptx 侧工作。

| 能力点 | 实测结论 | 证据 | 待办 |
|---|---|---|---|
| 页序读取（presentation rels 顺序，非文件名排序） | ✅ 已实测可用 | `reader_test`：SlideID/Part 顺序一致 | FEAT-003 仅剩隐藏页项 |
| 正文/备注/表格/图表/图片提取 + 坐标 Bounds | ✅ 已实测可用 | `reader_test`：文本与备注提取成功 | — |
| 隐藏页标记 | ❌ 缺口：IR 与公开 API 不暴露 `sldId@show=0` | `model.Page.Hidden=nil` 表示无法确定 | **FEAT-003 待办 2** |
| 未知/未解析形状（SmartArt/OLE/公式）标记 opaque | ✅ 已实测可用 | `isOpaqueKind` 映射 | — |
| p:timing 存在性（动画/计时） | ✅ 已实测可用 | `Page.HasTiming` | — |
| p:transition advTm（自动切页时长）读侧 | ❌ 缺口：TransitionSpec 仅暴露 AdvanceClick | 已从模型移除 | **FEAT-003 子项** |
| 配音写入：TrackKey 幂等更新 | ✅ 已实测可用（added/updated/unchanged） | `narrationwriter_test` 幂等用例 | — |
| 配音写入：自动播放（PlaybackOnSlideEnter）+ SetAdvanceAfter | ✅ 已实测可用（合成回读/Validate 0 错误） | `narrationwriter_test` 回读用例 | — |
| 已有时序树合并/保留、隐藏图标、真机客户端播音 | ⚠️ 未验证：需 go-pptx 侧与 PowerPoint 真机 | — | **FEAT-001 待办 4/验收** |

### FEAT-001 带音频 PPTX 配音完整化

- 类型：feature｜优先级：P0｜登记：2026-09-12
- 状态：NEW（G0 部分验证：TrackKey 幂等/自动播放已实测可用；真机门禁未过）
- 关联：ppts 开发计划 G0-8（带音频 PPTX 门禁）；V4.0 §9.3、§5.3 P 级
- 背景：go-pptx Play 维度为 Partial（配音受限 + timing 只读透传），无法支撑"可编辑配音 PPTX"门禁。

**G0 验证（2026-09-12）**：ppts `NarrationWriter`（go-pptx `UpsertNarration` + `PlaybackOnSlideEnter` + `SetAdvanceAfter`）已通过回读解析、go-pptx Validate 0 错误、同 TrackKey 幂等重跑断言。**剩余缺口**：已知 timing 树合并/保留、隐藏图标、`SetAdvanceAfter` 写出的 advance 元素在 PowerPoint 真机的自动切页行为，须真机门禁确认。

**描述**：补齐/验证写侧配音链路，使 ppts 能向副本 PPTX 绑定音频与计时：
1. 新增音频对象与页级关系（`AddAudio` 已有能力先行验证）
2. 写入符合目标客户端的媒体关系与播放时间节点（TrackKey 或等价系统标识，重跑只更新同一逻辑音轨）
3. 通过 `p:timing` 处理页内定时事件；通过 `p:transition` 的 `advTm` 设置自动切页（单位毫秒）；不虚构通用 `slideTiming` 节点
4. 保留并合并已有 timing 树，不覆盖用户动画；配置放映使用计时，验证点击与自动模式
5. 时序读侧从"只读透传"评估是否升级为结构化读写（见 API 影响）

**验收标准**：
- [ ] 认证客户端（PowerPoint 具体版本登记）打开后音频自动开始、切页不串音、重新进入页面正常、隐藏图标、末页结束、重开保存后仍正常
- [ ] 重跑配音仅更新同一逻辑音轨，不产生重复音轨
- [ ] 已有时序树（用户动画）字节保留，未被覆盖
- [ ] 支持度显式声明：Play 维度档位提升或"配音受限"边界明确写入能力矩阵
- [ ] ppts 适配器契约测试通过（G0-8 门禁）

**API 影响**：优先 API 默认/Experimental 新增；不破坏 Stable。触达 `AudioShape`（Stable）时仅追加方法。

### FEAT-002 讲稿提取增强（备注过滤与阅读顺序）

- 类型：feature｜优先级：P0｜登记：2026-09-12
- 状态：NEW（G0 备注/文本/图表/坐标提取已实测可用；阅读顺序与备注过滤未实现）
- 关联：ppts 开发计划 G1-2；V4.0 §5.2

**G0 验证（2026-09-12）**：备注提取（`SpeakerNotesText`/IR NotesText）、正文/表格/图表形状文本、坐标 Bounds 已实测可用。**剩余**：备注母版页脚/页码过滤（FEAT-002 项 1）、阅读顺序建议（项 2）需要 go-pptx Inspect 扩展；图表嵌入数据/单位读取在 G1 解析流水线中进一步验证。

**描述**：Inspect 维度增强，服务于讲稿提取：
1. 备注正文提取时过滤备注母版页脚、页码等非讲稿内容
2. 阅读顺序辅助：用坐标、组结构和文本语义生成建议顺序（对象顺序不必然等于阅读顺序）
3. 图表优先读取嵌入数据与坐标轴/单位；无法可靠提取时降低置信度

**验收标准**：
- [ ] 备注页脚/页码/母版模板内容不进讲稿正文
- [ ] 阅读顺序建议可输出且可被 ppts 覆盖校正
- [ ] 图表数据/单位可读；不可靠时显式降置信
- [ ] 36 样本 validate 0 错误，新增能力矩阵登记

**API 影响**：API 默认新增只读视图/报告字段；不破坏 Stable。

### FEAT-003 隐藏页报告与页序读取确认

- 类型：feature｜优先级：P0｜登记：2026-09-12
- 状态：NEW（页序已实测覆盖；隐藏页为真实缺口）
- 关联：ppts 开发计划 G1-2；V4.0 §5.2（真实页序不依赖 slide 文件名排序）

**G0 验证（2026-09-12）**：页序（presentation 关系顺序）经 IR `SlideID`/`Part` 顺序已实测可用，ppts 侧仅需契约测试。**确认缺口**：隐藏页标记（`sldId@show=0`）不在 IR `Page` 与公开 Slide API 中，ppts 以 `Hidden=nil` 表示无法确定；`p:transition advTm` 自动切页时长读侧同样未暴露（`TransitionSpec` 仅含 `AdvanceClick`）。

**描述**：
1. 按 presentation 关系读取真实页序（已由 Inspect Supported 覆盖则仅做契约确认测试）
2. 隐藏页状态报告（供 ppts 默认跳过、可选纳入）
3. 保留原页 ID 与原页码映射

**验收标准**：
- [ ] 页序与 presentation.xml 关系顺序一致，非文件名排序
- [ ] 隐藏页标记可读，ppts 可按策略跳过/纳入
- [ ] 页 ID 与页码映射稳定可追踪

**API 影响**：如 Inspect 已覆盖，仅 ppts 契约测试，不产生 go-pptx 变更（届时标记 REJECTED/CLOSED 并注明"无需改动"）。

### BUG 队列（模板，适配器测试阶段登记）

- 占位：ppts 适配器契约测试（`internal/project` / `internal/integrations`）期间发现的实际缺陷按 §4 模板登记到本节。
- 登记后按优先级进入 IN_PROGRESS；P0 缺陷阻塞对应里程碑放行。

### BUG-001 ADR-017 重构中间态残留 `validateChartData` 双声明，根包编译失败

- 类型：bug（重构中间态）
- 状态：NEW
- 优先级：**P0**（阻塞 ppts 本地全量构建与会话验证）
- 登记日期：2026-09-12
- 提出者：ppts（关联任务：G1 Connect API / 所有 `go build ./...`）
- 关联需求：ADR-017（chart 实现抽 `internal/chart`，根包保留薄包装）

#### 描述
go-pptx 本地产库（`/mnt/e/projects/go-pptx`，ADR-017 重构进行中，改动未提交）中，
`validateChartData` 被**同时定义两次**：
- `chartfrag.go:37`：新薄包装，委托 `chartinternal.ValidateChartData`（预期保留）
- `chart.go:~331`：旧完整实现（预期应删除但残留）

现象：`go build ./...` 报 `validateChartData redeclared in this block`，ppts 本地依赖（go.work 指向本地产库）整体不可编译。调用方 `chart.go:228/881`、`bind.go:826` 无感知，仅需保留一个定义。

#### 复现
- 最小样例：`cd /mnt/e/projects/go-pptx && go build .`
- 预期行为：编译通过（`internal/chart.ValidateChartData` 生效）
- 实际行为：`chartfrag.go:33:6: validateChartData redeclared in this block`（`../go-pptx/chart.go:331:6` 处另一声明）
- 环境：go-pptx 本地产库当前 working tree（3537adf 之后未提交改动）

#### 验收标准（可测断言）
- [ ] `cd /mnt/e/projects/go-pptx && go build ./...` 通过
- [ ] `go test ./...` 全绿（含 `-tags=corpus`）
- [ ] `rg "func validateChartData" --glob '*.go'` 仅 1 处（chartfrag.go 薄包装）

#### API 影响（ADR-015）
- 触达类型：无导出符号变化（`validateChartData` 为包内私有函数）
- 新增导出符号：无

#### 预估影响文件
- `chart.go`：删除残留旧 `validateChartData` 函数体

#### 完成记录
- PR / commit：待 code-buddy 填写
- ppts 验收结果：待 BUG-001 RESOLVED 后 `go build ./...` + 全量测试验证
- **ppts 侧规避（BUG-001 修复前）**：本地构建/测试使用 `GOWORK=off`（锁定发布 tag `github.com/F31/go-pptx v1.0.1`，与 CI 一致），不复用本地产库；go.work 保持指向但暂不生效。BUG-001 RESOLVED 后解除。

## 8. 版本与仓库约定

- 仓库：ppts = `/mnt/e/projects/ppts`（remote `https://github.com/F31/ppts.git`）；go-pptx = `/mnt/e/projects/go-pptx`（remote `git@github.com:F31/go-pptx.git`）。
- 开发期：ppts 根目录 `go.work` 指向 `/mnt/e/projects/go-pptx`；本地改动即时可见，不打断验证。
- 发布期：go-pptx 打正式 tag（`v1.1.0`…）后，ppts `go.mod` 锁定 tag；CI 只从 tag 拉取。
- 依赖可用判定：只有条目 VERIFIED 且 ppts 里程碑验收通过后，才视为依赖升级可用；`go.work` 指向但未验收的变更不构成交付依据。
- go-pptx 本地产库与 origin 的提交/推送由 code-buddy 负责；ppts 不越界操作 go-pptx 仓库。

## 9. 变更记录

| 日期 | 变更 |
|---|---|
| 2026-09-12 | V1.0 建立：流程、模板、质量门禁、初始队列 FEAT-001/002/003 + 空 BUG 模板 |
