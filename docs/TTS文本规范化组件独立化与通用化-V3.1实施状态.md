# 中文 TTS 文本规范化组件独立化与通用化 V3.1 实施状态

状态跟踪文件，对照《TTS 文本规范化组件独立化与通用化设计 V3.1》§15 实施阶段与 §16 最终验收定义。

核对日期：2026-09-28
核对依据：实际代码 `textnorm/`、`textnorm/cmd/textnorm` 与测试输出；未接入 PPTS 生产路径。

## 实施阶段（设计 §15）

| 阶段 | 交付 | 状态 | 备注 |
|---|---|---|---|
| S0 事实与基线审计 | 实现清单、旧行为语料、依赖图 | 部分完成 | V3.1 设计文档记录审计结论；N0/N1 历史行为仍待真实证据 |
| S1 契约冻结 | 输入输出、区间、错误、提示、配置版本规范 | 大部分完成 | Request/Result/Range/Candidate/Compile 已冻结；显式语义提示（Hints）已实现 |
| S2 独立模块骨架 | 独立 module、标准库核心、最小示例、CI | 完成 | `textnorm/` 独立 go.mod（仅标准库），可脱离 PPTS 构建测试；已加入 go.work 与独立 CI job |
| S3 执行与映射核心 | 候选识别、保护、冲突裁决、一次输出、来源关系 | 完成 | 非级联、保护区间、冲突诊断、identity/replacement/deletion 映射、取消语义 |
| S4 规则与配置迁移 | 基础中文类别、模板规则、版本化配置组合 | 部分完成 | 已迁日期/百分比/单位/小数/版本/型号/词典/**数字（数量/年份）**；缺金额、电话、比分、比率 |
| S5 PPTS 适配 | 数据库加载、标记兼容、双轨比较、缓存版本化 | 部分完成（narrator 影子已接入） | `internal/textnormshadow`（Builder/Compare/存储映射）＋批量 CLI `cmd/textnorm-shadow`；`app.NewTextNormShadowEngine` 与旧引擎共用同一持久化配置；narration 合成路径接入影子比较（`synthesizeSegment` 每段 diff，仅观测不改输出/缓存键），以 `PPTS_TEXTNORM_SHADOW=1` 显式启用（默认关闭），影子构建/运行失败不阻塞任务，分歧计数进 `ppts_textnorm_shadow_diff_total{diverged,error,build_error}`；**主引擎仍为旧 `internal/textnorm`，未切换** |
| S6 独立发布 | `v0.x` 发布、文档、示例、行为差异报告 | 未开始 | CLI `run`/`diff` 已具备行为差异报告雏形 |
| S7 稳定化 | 真实音频验收、扩展 API 验证、兼容承诺 | 未开始 | |

## 最终验收定义（设计 §16）

| 验收项 | 状态 | 证据 |
|---|---|---|
| 可在 PPTS 之外独立运行 | 通过 | `textnorm/` 独立 go.mod（仅标准库）；`cd textnorm && go test ./...` 独立通过；CLI 无 PPTS 导入 |
| 核心不依赖数据库、租户、HTTP、环境变量和 TTS 供应商 | 通过 | 包内无相关导入 |
| 规则不会隐式处理本次执行生成的文本 | 通过 | 非级联执行模型；`TestNormalizeDoesNotCascadeRuleOutput` |
| 型号、版本号等完整表达不会被通用数字规则拆坏 | 通过 | `TechnicalModelRule`、`ZHVersionPreserveRule`；negative/overlap 语料 |
| 类别停止转换后，仍可保留识别保护能力 | 通过 | `ModePreserve`；date-preserve 语料 |
| 歧义处理规则公开、确定且可测试 | 通过 | 冲突/保护/提示诊断与保留语义可测；显式提示（Verbatim/Reading/Semantic）已实现 |
| 模板规则与语义转换规则能力边界清楚 | 通过 | `RegexRule`（模板）与日期/小数等语义规则职责分离 |
| 来源映射不把比例估计标成精确关系 | 通过 | identity/replacement/deletion 区分；无比例投影冒充精确 |
| 读音及停顿提示具备结构化接口 | 通过 | 结构化 Hints（verbatim/reading/semantic）＋旧标记兼容（`〔读：x〕`/`‖`）＋停顿 Control 事件＋`StripLegacyMarkers`/`DisplayToEffective` |
| 公共 API 与输出行为都有版本策略 | 部分 | `Manifest` 记录规则集；语义化版本与配置组合契约未正式化 |
| 规则更新可预览、追踪、切换与回滚 | 部分 | 内置 profile 可切换；CLI `diff` 报告行为差异；热更新/快照回滚未做 |
| 基础语料、反例、交互和并发测试通过 | 通过 | JSON 语料 21 用例＋引擎不变量 Go 测试＋race 通过 |
| PPTS 完成真实音频集成验收，且与核心文本正确性分开报告 | 未完成 | 生产路径未切换 |

## 当前提交范围

独立模块批次（与并行限流/审计工作隔离）：
- `textnorm/`（独立 go.mod：候选引擎、规则、语料、profile、测试、CLI、README）
- `docs/TTS文本规范化组件独立化与通用化设计-V3.1.md`
- `.github/workflows/ci.yml`（新增 textnorm job）

## 下一步候选

1. 用影子分歧归零/评审做切换门：当生产影子（`PPTS_TEXTNORM_SHADOW=1`）采集的分歧→0（或逐条评审接受）后，把主引擎切换到候选引擎并废弃 `internal/textnorm`。
2. 补候选引擎 money/电话/比分/比率类别，继续缩小影子分歧面。
3. 语义化版本与配置组合契约正式化（S6 前置），随后 `v0.1.0` tag + 移除 monorepo 本地 replace。
4. S7 稳定化：真实音频验收与扩展 API 验证。
