# ADR-027：数字形态判定留在 `internal/validation`，不进 textnorm

- 状态：已采纳（2026-11-02：记录评审结论；代码注释见 `internal/validation/counts.go` 头注释）
- 关联：`internal/validation/counts.go`、`internal/validation/rules.json`（`counts` 段）、`textnorm`（`github.com/F31/textnorm`）
- 编号说明：承接 ADR-020 之后续编为 027。021–026 已由《动画视频扩展-设计方案》占用（本地文档，不随仓库分发），故不占用。

## 背景

一键成稿的事实保真门禁把「目标里多出来的数字」按形态分流：实测事实（64GB、40%）严格比对，
概括计数（「4 类场景」）放行并提示，序号（「第 2 步」）默认拦截。形态在抽取阶段由
`numberForm` 判定一次，处置在 `rules.json` 的 `counts` 段声明。

评审时提出：既然 `textnorm` 是「文本理解/归一」模块，形态判定是否更适合放在
`textnorm`（`E:\projects\ppts\textnorm`，独立模块 `github.com/F31/textnorm`）。

## 决策

**形态判定与处置策略都留在 `internal/validation`，不迁入 `textnorm`。**

`textnorm` 只修自己的读音缺口：`zh.ordinal` 规则补上 `No.3` / `#2` 两种序数写法
（此前只有 `第N`，`No.3` 会落到数字规则读成「No.三」）。

## 判据

| 维度 | `internal/validation`（事实保真门禁） | `textnorm`（TTS 读音契约） |
|---|---|---|
| 分类轴 | 该不该拦：fact / count / ordinal | 怎么念：date / decimal / percent / telephone |
| 分类依据 | 业务裁定（序号不豁免是明确裁定） | 读音歧义（`1/3` 是分数还是日期） |
| 变更驱动 | 门禁口径、`rules.json` 版本、回放基线、提示词 | 公开契约、profile 版本、`ConfigHash` |
| 消费者 | 仅 `internal/app/scriptdraft.go` + 回放工具 | app 播音链路、外部调用方 |

1. **两条正交的分类轴，合并后谁都用不顺手**：`textnorm` 永远用不到 `count`，门禁也永远用不到
   `telephone`。同名枚举装两套语义，只会制造「看起来统一、实际各判一次」的错觉。
2. **变更驱动不同，合并会把门禁绑到 TTS 的发布节奏上**：改一句计数后缀表属于门禁口径变更，
   需要同步回放基线与提示词；在 `textnorm` 侧则意味着 profile 版本与 `ConfigHash` 变化。两者
   审批与留痕的责任人不同。
3. **依赖方向**：`internal/validation` 只 import `regexp/sort/strings/unicode/utf8`，是能单独推理、
   单独回放、可整体移植的纯包。让安全门禁依赖被 TTS 需求推动演进的公开模块，等于把门禁的
   可复现性交给另一条产品线的节奏。
4. **只有一个消费者**：抽到独立模块的价值来自第二个消费者，现在抽是预付成本。

真正重复的只有**词表**（序号前缀、单位、量词族），不是判定逻辑。词表可以各自演进——事实也
证明了两侧本来就各缺一块（`rules.json` 认 `No.`/`#` 为序号，`textnorm` 的 `zh.ordinal` 当时只认 `第`）。

## 备选方案

- **整体搬进 textnorm**：`textnorm` 的公共契约要新增「文档语义角色」概念，与它「不依赖 PPTS 应用
  关切」的定位冲突；且每次门禁微调都要走模块版本流程。不采用。
- **半拆（判定留这边、词表放那边）**：最差的一种。`FormatCountHint` / `FormatEquivalenceHint`
  与校验器共用同一份策略，是这套门禁的核心不变量（指令说可以、校验器必须接受）；词表跨模块后
  两侧版本不同步即产生「照着指令写却被判违规」的假失败。不采用。
- **现在就把 validation 整体抽成独立模块**：没有第二个消费者，属于预付成本。不采用，但**保留**
  这条路——真的出现第二个消费者时，判定逻辑 + `rules.json` + 回放语料必须一起走。

## 重估条件

- 出现第二个消费者（别的服务也要做源/目标保真判定）→ 整体抽 `internal/textcheck` 或独立 module，
  不是拆一半。
- `textnorm` 将来需要按语义角色选择读音（例如计数与序数的读法分化）→ 那时它需要自己的
  count/ordinal 分类，仍不复用本文的枚举。

## 验收

- `counts.go` 头注释写明边界与判据（避免同一问题被重复讨论）。
- `zh.ordinal` 对 `第12章` / `No.3` / `#2` 均输出中文序数读法；`#2024`、`C#2`、`piano.3` 不被误读
  （有 Go 测试断言）。
- 门禁行为零变化：`internal/validation` 的用例与回放基线不因本次调整而翻转。
