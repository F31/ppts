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
| S4 规则与配置迁移 | 基础中文类别、模板规则、版本化配置组合 | 完成 | 已迁日期/百分比/单位/小数/版本/型号/词典/数字/金额/**电话**/**比分（语义提示门控）**；通用中文类别主力已齐 |
| S5 PPTS 适配 | 数据库加载、标记兼容、双轨比较、缓存版本化 | 完成（生产影子已启用） | `internal/textnormshadow`（Builder/Compare/存储映射）＋批量 CLI `cmd/textnorm-shadow`；narration 合成路径接入影子比较（仅观测不改输出/缓存键）。**已部署 ec5115a 并开启 `PPTS_TEXTNORM_SHADOW=1`**，分歧计数进 `ppts_textnorm_shadow_diff_total{diverged,error,build_error}`（/metrics 已注册，worker 日志确认启用）；主引擎仍为旧 `internal/textnorm`（切换门=影子分歧归零/评审） |
| S6 独立发布 | `v0.x` 发布、文档、示例、行为差异报告 | 完成 | 语义化版本契约已加（`Version`、`ModulePath`、`Profiles()`/`ProfileVersion`，Manifest 携带版本）；组件发布于 `github.com/F31/textnorm`，tag `v0.1.0` 已被 go 代理索引；**外部最小消费者经 `go get github.com/F31/textnorm@v0.1.0` 独立构建运行成功**；monorepo 保留本地 replace（CI 稳定性） |
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

1. 生产影子采集：部署含影子接线的二进制后，开启 `PPTS_TEXTNORM_SHADOW=1`，采集分歧归零/评审后切换主引擎并废弃 `internal/textnorm`。
2. S7 稳定化：真实音频验收与扩展 API 验证。
