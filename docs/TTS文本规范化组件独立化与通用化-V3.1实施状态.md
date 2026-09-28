# 中文 TTS 文本规范化组件独立化与通用化 V3.1 实施状态

状态跟踪文件，对照《TTS 文本规范化组件独立化与通用化设计 V3.1》§15 实施阶段与 §16 最终验收定义。

核对日期：2026-09-28
核对依据：实际代码 `textnorm/`、`textnorm/cmd/textnorm`、根模块回归与生产切换验证。

## 实施阶段（设计 §15）

| 阶段 | 交付 | 状态 | 备注 |
|---|---|---|---|
| S0 事实与基线审计 | 实现清单、旧行为语料、依赖图 | 部分完成 | V3.1 设计文档记录审计结论；N0/N1 历史行为仍待真实证据 |
| S1 契约冻结 | 输入输出、区间、错误、提示、配置版本规范 | 大部分完成 | Request/Result/Range/Candidate/Compile 已冻结；显式语义提示（Hints）已实现 |
| S2 独立模块骨架 | 独立 module、标准库核心、最小示例、CI | 完成 | `textnorm/` 独立 go.mod（仅标准库），可脱离 PPTS 构建测试；已加入 go.work 与独立 CI job |
| S3 执行与映射核心 | 候选识别、保护、冲突裁决、一次输出、来源关系 | 完成 | 非级联、保护区间、冲突诊断、identity/replacement/deletion 映射、取消语义 |
| S4 规则与配置迁移 | 基础中文类别、模板规则、版本化配置组合 | 完成 | 已迁日期/百分比/单位/小数/版本/型号/词典/数字/金额/**电话**/**比分（语义提示门控）**；通用中文类别主力已齐 |
| S5 PPTS 适配 | 数据库加载、标记兼容、双轨比较、缓存版本化 | 完成（生产主路径已切换） | `internal/textnormshadow`（Builder/Compare/存储映射）＋批量 CLI `cmd/textnorm-shadow`；narration 合成路径已从旧 `internal/textnorm` 切换为模块引擎 `github.com/F31/textnorm`，旧引擎保留为影子观测。**已部署 c2261cf 并开启 `PPTS_TEXTNORM_SHADOW=1`**。过程中补齐两处基建：① worker 进程新增受 `PPTS_METRICS_TOKEN` 保护的 `/metrics`（影子计数在 worker 进程内自增，api 的 `/metrics` 看不到），地址 `127.0.0.1:8090`=PPTS_WORKER_METRICS_ADDR；② `BuildNewEngine` 补齐 S4 规则装配（TechnicalModel/VersionPreserve/Telephone/Score 门控），修正"0 分歧其实是未比较"的盲区。受控注入 7 段真脚本（测试租户＋本地 mock TTS，零外部成本），切换前旧主/新影子与切换后新主/旧影子均捕获 **3 处分歧**（见 §采集结论），与离线复算一致；均为候选引擎改进方向并已评审接受。 |
| S6 独立发布 | `v0.x` 发布、文档、示例、行为差异报告 | 完成 | 语义化版本契约已加（`Version`、`ModulePath`、`Profiles()`/`ProfileVersion`，Manifest 携带版本）；组件发布于 `github.com/F31/textnorm`，tag `v0.1.0` 已被 go 代理索引；**外部最小消费者经 `go get github.com/F31/textnorm@v0.1.0` 独立构建运行成功**；monorepo 保留本地 replace（CI 稳定性） |
| S7 稳定化 | 真实音频验收、扩展 API 验证、兼容承诺 | 部分完成 | 最小真实音频验收已通过；扩展真实音频验收已覆盖金额/日期/百分比/电话/型号版本/停顿读音标记/比分无提示样本（5 段正式 TTS，job succeeded）。仍待扩展 API 验证与更长观察期。 |

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
| 公共 API 与输出行为都有版本策略 | 通过 | `Manifest` 记录 module `Version`、profile、profileVersion、configHash 与规则集；`CompileProfile` 作为外部消费者推荐入口；外部 consumer（Go 1.24.2 + replace）验证通过 |
| 规则更新可预览、追踪、切换与回滚 | 部分 | 内置 profile 可切换；CLI `diff -format json` 输出 base/next manifest 与行为差异，可作为预览/审计基线；生产热更新/快照回滚未做 |
| 基础语料、反例、交互和并发测试通过 | 通过 | JSON 语料 21 用例＋引擎不变量 Go 测试＋race 通过 |
| PPTS 完成真实音频集成验收，且与核心文本正确性分开报告 | 部分 | 生产文本规范化主路径已切换；mock TTS 7 段验证通过；正式 TTS 已完成 1 段最小验收 + 5 段扩展验收，仍待人工听感/字幕抽检 |

## 当前提交范围

独立模块批次（与并行限流/审计工作隔离）：
- `textnorm/`（独立 go.mod：候选引擎、规则、语料、profile、测试、CLI、README）
- `docs/TTS文本规范化组件独立化与通用化设计-V3.1.md`
- `.github/workflows/ci.yml`（新增 textnorm job）

## 生产影子采集结论（2026-09-28）

- 受控注入：测试租户（`textnorm-shadow-inject`）+ 1 项目 + 1 slide + 7 分段真脚本（含数量/日期/金额/电话/比分/PM2.5/型号/版本串），本地 mock TTS（HTTP 127.0.0.1:18090，零外部调用），worker 真实执行 narration 合成路径。
- 观测：`ppts_textnorm_shadow_diff_total{diverged}` 在生产 worker 进程（`127.0.0.1:8090`）捕获 **3 处分歧**，与 `cmd/textnorm-shadow` 离线复算（3/7）完全一致。
- 分歧明细（全部为候选引擎对旧引擎的改进，评审接受）：
  1. `13800138000` → 旧：一百三十八亿零一十三万八千（数量误读）；新：一三八零零一三八零零零（优先逐位读，`ZHTelephoneRule`）。
  2. `PM2.5` → 旧：PM2.五；新：PM2.5（`2.5` 作为技术型号串被保留，`TechnicalModelRule`）。
  3. `v3.1.2` → 旧：v3.一.二；新：v3.1.2（版本串保留，`ZHVersionPreserveRule`）。
- `3:2` 未分歧：比分规则为语义提示门控（无 `SemanticScore` 提示时保守不触发），符合设计。
- 基建补全：worker 独立 `/metrics`（`PPTS_WORKER_METRICS_ADDR`，Bearer 鉴权 fail-closed）；`BuildNewEngine` 补齐 S4 规则装配。
- 主引擎切换：`c2261cf` 已部署为生产二进制（SHA `a5ec3590670019ae665030970c88e8c5611f7a4493529c5d5e92e4e7dc265b2c`），模块引擎负责输出/缓存键，旧引擎作为影子观测；切换后重新注入 7 段，job 成功，worker 新进程指标再次捕获 `ppts_textnorm_shadow_diff_total{diverged}=3`。
- S7 最小真实音频验收：正式 gateway 租户 job `35014d84-b3d8-4afc-bc38-9a4c9bcf10b1` 成功，生成 `audio/wav`（约 284KB）、timeline、SRT/VTT 与 alignment；`ppts_tts_synthesis_total{kind="narration",event="succeeded"}` 从 3 增至 4，`ppts_textnorm_shadow_diff_total{diverged}` 从 4 增至 5（该短段预期电话规则 1 处分歧）。
- S7 扩展真实音频验收：正式 gateway 租户 job `ba05498e-e440-4e05-94bb-ec87d6f833ac` 成功，5 段生成真实 WAV、timeline、SRT/VTT 与 alignment；`ppts_tts_synthesis_total{kind="narration",event="succeeded"}` 从 4 增至 9，`ppts_textnorm_shadow_diff_total{diverged}` 从 5 增至 7。覆盖金额/日期/百分比/电话/型号版本/停顿读音标记/比分无提示样本；quota reservation 已 settled，`reserved_units` 回到 0。
- 旧引擎处置：暂不删除 `internal/textnorm`；冻结为 shadow-only 对照实现。移除门槛：至少完成扩展 API 验证、人工听感/字幕抽检，并观察切换后 shadow 指标无新增未评审分歧。
- API/预览补强：`github.com/F31/textnorm` 增加 `CompileProfile`、`Engine.Manifest()` 与 `Manifest.ConfigHash`；CLI `diff -format json` 输出 base/next manifest，外部 consumer 验证通过（Go 1.24.2）。
- 运维噪音：生产已补授 `ppts_queue_backlog_seconds()` 给 `ppts_app`/`ppts_scheduler`；代码层跳过跨租户 worker 的空 tenant queue-depth 采样，避免空 UUID 日志。待新版 worker 部署后验证日志消失。
- 人工验收：机器检查已确认 5 个 WAV 可解码（3.52s/3.60s/3.96s/5.76s/6.96s），VTT/SRT/timeline 均生成且覆盖 5 段；仍需人耳确认读音、停顿体感与字幕对齐主观质量。

## 下一步候选

1. 部署队列指标噪音修复后二次观察 worker 日志。
2. 人工听感与字幕抽检通过后，再移除或归档旧 `internal/textnorm`。
