# 设计：讲稿侧结构词如何参与分档

## 判据

```
layoutOK = (编号标记数 ≥ 3) || 存在卡片簇          // 与现状相同
scriptOK = 讲稿任一段含结构词

structured     ⟸ layoutOK && scriptOK
low-confidence ⟸ 其余，bandReason 按缺哪一侧给
```

`scriptOK` 的结构词与三条规则**同一口径**（复用 `ordinalRe` / `qtyMatch` / `dirRe` /
`tableRefs` / `imageRefs` / `centerWords`）：

| 类 | 例子 | 对应规则 |
|---|---|---|
| 序数 | 第二部分、第三步 | `ordinal` |
| 精确数量 | 分五个部分、四条 | `count`（`detectCount`，2..12） |
| 空间指代 | 右边那张图、这张表、中间 | `spatial` |

排除序数形态的数量词（「第二条」里的「二条」）：与 `qtyMatch` 的前字符检查同一套逻辑。
放宽到「含任何数字+量词」会让「第 2 条」同时点亮序数与数量两个信号，而它们对应的是同一处
版面结构，重复计入没有意义。

**数量词必须走 `detectCount` 而不是 `qtyMatch`**：后者只做形态匹配，「毛利率比上季度高
一个点」会点亮数量信号，而「一个X」根本不编码版面结构（版面不会有 1 项的列表）。
`detectCount` 自带 2..12 的下限，正是 `countBind` 的口径。这个坑是第一版实现踩到的——
API 用例 `TestPrefillBandReadsNarration` 的「口语直讲」一格被判成 `structured`，查下来是
讲稿里那句「高一个点」被当成了数量概括。口径不对齐，分档会说「有信号」而规则一条也绑不上。

## bandReason

| 值 | layoutOK | scriptOK | 预期内容召回 | 用户该改什么 |
|---|---|---|---|---|
| （空，band=structured） | 是 | 是 | 0.972 | — |
| `layout` | 否 | 是 | 0.533 | 版面（加编号/卡片），或逐条核对 |
| `script` | 是 | 否 | 0.667 | 讲稿（把「第几条」「分几块」讲出来），或逐条核对 |
| `both` | 否 | 否 | 0.533 | 两边都可，优先核对 |

`layout` 与 `both` 的实测召回相同（都是 0.533 那一档），之所以分开：讲稿有结构词说明用户
已经按顺序讲了，只是版面没编码；两条路线的改法完全不同，合成一个值会让提示说错方向。

## 信号

`Signals` 顺序固定：`markers` / `clusters` / `directions` / `script-structure|script-flat`。

`directions` 仍然只作信号、不参与定档（恒真问题在上一个变更里已记）。新增两条互斥，永远只
出一条：讲稿有结构词出 `script-structure`，没有出 `script-flat`。

## 文案

低置信三句，按 `bandReason` 选一句。旧 key `editor.animationPrefillLowConfidence` 拆成三条，
因为原文案写死了「这一页没有检测到结构化版面信号」——在 `script` 这一档，版面上明明有信号，
留着旧文案等于对用户说假话。

- `...Layout`：这一页没有检测到结构化版面信号（编号标记或卡片簇），讲稿也没把结构讲出来。
  预填只有字面依据：实测内容召回 0.533，结构化版面是 0.972。请逐条核对。
- `...Script`：**版面**有结构（编号或卡片），但**讲稿**没有把它讲出来（没有「第几条」「分几块」
  这类说法）。预填只能靠字面与顺序：实测内容召回 0.667，低于结构化页面的 0.972。请逐条核对。
- `...Both`：版面没有结构信号，讲稿也没有把结构讲出来。实测内容召回 0.533。请逐条核对。

`...Script` 是第一句必须**指出改哪里**的提示：不说清楚，用户会去重排 PPT（版面没问题），
而真正该动的是讲解词。

## 兼容性

- `q8-script.json` 含 13 处结构词、版面有编号与卡片 → 仍判 `structured`，金标 0.972/0.962
  与来源分布一条不动。这一点用既有 `TestGoldenQ8` 守住，不需要新断言。
- `SpecFromElements` / `DefaultSpecReport` 两个入口的等价性测试不受影响：分档不进规格。
- API `animationPrefillDTO` 增 `bandReason`（omitempty）；`Band` 取值不变，老前端只认
  `band` 时行为退化为「按低置信统一提示」，不会崩。

## 已知边界

`scriptOK` 是**弱前提**：它只说明讲稿提到了结构，不保证后面真的逐条讲。消融里目录页就是
「一句话说分五个部分」然后一句话带过十一个元素——这一句同时点亮了 `count` 规则和
`scriptOK`，撑起整页 11 条绑定里的 10 条。所以「script-structure + structured」不是质量保证，
只是「规则有信号可用」的必要条件。这个限制写在这里，不假装判据比它实际能做的更多。

## 测试点

1. 版面有结构 + 讲稿无结构词 → `low-confidence`、`bandReason=script`、信号含 `script-flat`。
2. 版面有结构 + 讲稿有结构词 → `structured`、无 `bandReason`、信号含 `script-structure`。
3. 版面无结构 + 讲稿有结构词 → `low-confidence`、`bandReason=layout`。
4. 版面无结构 + 讲稿无结构词 → `low-confidence`、`bandReason=both`。
5. `「第二条」不点亮数量信号`：只有序数时 `script-structure` 仍出，但不得因此被当成两类计数。
6. 金标不回归：`TestGoldenQ8` 的分类计数与召回精确逐项相等。
7. 前端：三条文案按 `bandReason` 分叉，`script` 档必须出现「讲稿」二字（防止回退成版面文案）。
