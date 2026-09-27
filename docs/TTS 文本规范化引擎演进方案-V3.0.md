# TTS 文本规范化引擎演进方案 V3.0（对标 itntext FST 架构）

> 版本：V3.0（2026-09-27，设计稿；M0-M5 已实现，见「九、实现状态」）
> 上一版：V2.8（最终收敛，唯一入口 `Run` + 可靠性信号）
> 定位：参照 itntext（wetext 风格，继承 NeMo/WeTextProcessing 的「tagger → verbalizer」声明式 FST 架构），
> 在不引入 Python 依赖的前提下，把当前「函数规则表 + 手写扫描器」演进为「编译式规则流水线」。
>
> 本文档回答：当前实现相对 itntext 有哪些结构性缺陷，以及在不考虑重构成本的前提下，如何从
> 技术架构、落地效果、易用性、可维护性、性能五维改进。

---

## 〇、现状与目标基线

### 0.1 当前实现（V2.8，`internal/textnorm`）

- 规则模型：`type Rule func(*Context) (string, bool)`——函数闭包 + priority 排序 + 叠加语义。
- 扫描：内置 `scanSpans` 只识别显式定界符（`〔读：x〕`/`‖`），普通 span 交给规则表。
- 词典：`DictAdapter.Substitute` 逐条 `strings.ReplaceAll`（复刻 `pronunciation.Apply` 语义）。
- 数值：`NumberRule` 只处理 ≤12 位整数（数量位读 / 年份逐位），默认不注册。
- 映射：`IndexMap` 用「段 + 比例插值」（`MapSrcToDst` 对异长段做 `round(frac*span)`）。
- 性能：O(n) 单趟、冻结后并发安全，但 `expandDigits` 用 `[]rune` 转换 + 反复 `runeLen(sb.String())` 存在 O(n²) 隐患。

### 0.2 itntext 架构要点（对标对象）

- **声明式 grammar**：`grammars/*/tagger` + `verbalizer`，pynini/OpenFST 编译为 `.fst`。
- **两阶段流水线**：`tag`（识别待规范化子串 + 打类别标签）→ `parse`（token 解析）→ `verbalize`（按模板产出）。
- **类别隔离**：whitelist / number / percent / date / money / unit 各自 graph，不同类别不互相吞字。
- **数据驱动**：TSV 资源表（char width、denylist、whitelist、erhua），改词不改逻辑。
- **回归基线**：`reports/FULL_SCENARIO_COMPARE.csv` 全场景对比 + wetext 对照脚本。
- 代价：Python 运行时、FST 每次调用两遍 + 排列枚举，性能逊于 Go 单趟。

### 0.3 演进目标

对标 itntext 的「tagger→verbalizer + 类别隔离 + 数据驱动 + 场景基线」，全部用 Go 实现：
保留当前引擎的「单趟、冻结并发、零外部依赖」性能优势，不引入 Python sidecar。

---

## 一、技术架构演进：函数规则表 → 编译式规则流水线

### 1.1 缺陷（V2.8 现状）

- **A**：规则是函数闭包，新领域（日期/时间/金额/单位）每加一条都要手写状态机扫描，重复造轮子。
- **B**：词典是「整 span ReplaceAll」，与数值规则隐式耦合（线上实测 `5GHz→五吉赫兹`：词典先改 `GHz→吉赫兹`，数值后把 `5` 也展开）。
- **C**：`IndexMap` 比例插值在长段多处改写时累积误差。

### 1.2 新架构：Tagger → Verbalizer 流水线

```
普通 span
   │
   ▼
┌─────────────────────────────────────────────┐
│ Tagger（类别识别，正则自动机，编译期预编译）      │
│   t_number  /[0-9]+(?:\.[0-9]+)?/            │
│   t_percent /(...)%/                         │
│   t_date    /(\d{4})[-/.](\d{1,2})[-/.](...)/│
│   t_money   /(¥|￥|\$|A\$|HKD)?(...)/        │
│   t_unit    /(...)(kg|kg|°C|m²|ms|MHz|GHz)/ │
│   t_word    /(词典词条)/                      │
│   …按类别各自独立匹配，输出「打标文本」          │
└─────────────────────────────────────────────┘
   │  打标文本（如 `{percent:{num:"6.3"}} 提升`）
   ▼
┌─────────────────────────────────────────────┐
│ 类别隔离（按 token 边界切分，类别间不重叠）      │
└─────────────────────────────────────────────┘
   ▼
┌─────────────────────────────────────────────┐
│ Verbalizer（按类别模板产出中文读法）            │
│   percent → "百分之"+num2cn                    │
│   date    → 年份直读+月+日                      │
│   money   → 币种名+金额中文                     │
│   unit    → num+单位中文                        │
└─────────────────────────────────────────────┘
   │
   ▼
朗读文本（effectiveText）
```

#### 1.2.1 与 itntext 的对应

| itntext 组件 | V3.0 Go 实现 |
|---|---|
| grammar（pynini 声明） | `RegisterPattern(category, priority, *regexp.Regexp, verbalizer)` 声明式注册 |
| tagger FST | Go `regexp` 自动机（编译期 `regexp.MustCompile`，等价于预编译 FST） |
| TokenParser | 打标输出统一为「`{cat:{field:value}}`」结构，引擎解析为 token 列表 |
| verbalizer FST | 类别→中文读法模板（`Verbalizer func(fields map[string]string) (string,bool)`） |
| 类别隔离 | token 化切分，规则按「整 token 类别」触发，不跨类别吞字 |
| TSV 资源 | 运营后台可编辑的「词典 + 上下文规则表」（存 DB，见 §4） |

#### 1.2.2 接口草案

```go
// Pattern 是"编译式 tagger"：一条声明式规则。
// 每个 Pattern 只负责识别**一类**子串（number/percent/date/...），
// 输出统一打标结构；引擎按类别隔离后交给对应 Verbalizer。
type Pattern struct {
    Category   string // number / percent / date / money / unit / dict
    Priority   int    // 同 itntext grammar 优先级
    Match      *regexp.Regexp
    Groups     []string // 命名组名（value/unit/...），供 verbalizer 取字段
    Verbalize  func(ctx *Context, fields map[string]string) (string, bool)
}

// 引擎新增注册入口（替代裸 RegisterRule 的手写扫描）：
func (e *Engine) RegisterPattern(p Pattern) {
    // 校验 Match != nil、Category 非空、Verbalize 非 nil；冻结后 panic
}
```

#### 1.2.3 词典与数值的「token 隔离」解决缺陷 B

- 普通 span 先按「CJK 词 / 字母词 / 数字串 / 符号」切 token。
- `dict` 类 Pattern 只在**整 token** 上做词典匹配（不含数字串内部）。
- `number`/`percent` 等类只在**纯数字 token / 数字+单位边界**上触发。
- 结果：`5GHz` → 数字 token `5` + 字母 token `GHz`，类别各自处理，**不再互相吞字**。
- 黄金对比测试②（`Run` 输出与 `pronunciation.Apply` 逐字节一致）仅在「未注册 number 类」时成立，
  文档明确该约束范围（V2.8 §7 已确立"数值默认不改写"边界）。

#### 1.2.4 IndexMap 精确化（解决缺陷 C）

- 由「段 + 比例插值」改为「**逐 rune 精确映射表**」`[]int`：
  - `Verbalizer` 产出时通过 `ctx.RecordRune(srcIdx, dstIdx)` 精确登记每个输出 rune 的来源 rune。
  - 引擎在 `Run` 末尾拼装 `dispToEff []int`（长度=显示文本 rune 数，值=朗读文本 rune 下标），
    `MapSrcToDst` 退化为 `O(1)` 查表。
  - 保留旧 `IndexMap` 结构作为「无精确映射时的回退」（段落级比例），但默认走精确表。
- 内存成本：每段一个 `[]int`（几百字节级），对 worker 无感。

---

## 二、落地效果演进：补全 PPT 高频领域

### 2.1 缺陷（V2.8 现状）

- **D**：`NumberRule` 只处理 ≤12 位整数（数量/年份两种模式）。真实 PPT 讲稿高频的
  **小数、百分比、日期、金额、单位、分数** 全都不处理。
- **E**：数值展开「全有或全无」且默认关，无类别粒度开关。

### 2.2 领域覆盖计划（按 PPT 场景优先级）

| 类别 | 场景示例 | 期望读法 | 优先级 |
|---|---|---|---|
| percent | 同比增长 6.3% | 同比增长百分之六点三 | P0 |
| decimal | 售价 13.5 万 | 十三点五万 | P0 |
| date | 2002/01/28 上线 | 二零零二年一月二十八日上线 | P1 |
| money | 价格 ¥13.5 | 十三元五角（币种映射） | P1 |
| unit | 重达 25kg | 二十五千克 | P1 |
| fraction | 总量的 1/5 | 五分之一 | P2 |
| phone/serial | 拨打 12306 | 一二三零六 | P2 |
| math | 比分 78:96 | 七十八比九十六 | P3 |

每类 = 一条 `RegisterPattern`（识别 + 模板），**独立开关**：
`PPTS_TEXT_NORM_CLASSES=percent,decimal,date,money,unit`（默认空=仅标记+词典，保持 V2.8 零回退）。

### 2.3 中文数词模板（统一复用）

- `num2cn`（位读，含小数/负号/科学计数法）——重构现有 `quantityWords`，抽出为公共 `num2cn` 库，
  供 percent/date/money/unit 共享（消除重复手写）。
- `year2cn`（年份逐位）——复用现有 `yearRead`。
- 币种映射表（¥→人民币/元、$→美元、A$→澳元、HKD→港元）——数据驱动（§4）。

---

## 三、易用性演进：声明式 DSL + 数据驱动

### 3.1 缺陷（V2.8 现状）

- **F**：加一条新规则 = 写 Go 代码 + 手写扫描器；专有读法只能靠发音词典 ReplaceAll 硬编码，
  无法表达「上下文相关读法」。

### 3.2 声明式规则 DSL（Go 内嵌）

```go
// 内嵌 DSL：新增「价格」规则只需声明识别 + 模板，零手写扫描。
e.RegisterPattern(Pattern{
    Category: "money",
    Priority: 40,
    Match:    regexp.MustCompile(`(?:(¥|￥|\$|A\$|HKD)\s*)?([0-9]+(?:\.[0-9]+)?)\s*万?`),
    Groups:   []string{"currency", "amount"},
    Verbalize: func(_ *Context, f map[string]string) (string, bool) {
        return moneyRead(f["currency"], f["amount"]), true
    },
})
```

### 3.3 数据驱动词表（运营后台可编辑，对标 itntext TSV）

| 存储 | 内容 | 语义 |
|---|---|---|
| `pronunciation_dictionaries`（已有） | 字面替换词条 | 词典类 Pattern 的匹配源 |
| **新增 `contextual_rules` 表**（推荐） | pattern / 类别 / 产出模板 / 优先级 / 启用 / 租户 | 上下文相关读法（`%`→百分之、`kg`→千克、币种映射） |
| 结构性规则（日期/金额逻辑） | 仍 embed 代码 | 逻辑不改，词表可配 |

- 新增 `contextual_rules`：`(tenant_id, pattern, category, replacement, priority, enabled, created_at)`,
  `LoadContextualRules(tenantID)` → 追加为 `Pattern` 注册。运营后台管理界面可增删改（第二批）。
- 平台种子同理可喂上下文规则（对标平台默认词典）。

---

## 四、可维护性演进：阶段化 pipeline + 场景回归基线

### 4.1 缺陷（V2.8 现状）

- **G**：规则表是「隐式叠加 + 全局优先级」，`processSpan` 的「前规则改长度→后规则禁上报精配」
  是隐式状态机，顺序敏感；无 itntext 那种全场景回归 CSV 基线。

### 4.2 规则阶段化（消除隐式叠加状态机）

> 实现注记（2026-09-27）：`lengthChanged` 纪律**不是可删的隐式状态**——它是叠加模型下
> IndexMap 坐标对齐的正确性保证（前序规则改长度后，后续规则子段坐标不再对齐 span 原文）。
> M1 的 token 隔离已消除规则间最实际的隐式耦合（缺陷 B：5GHz→五吉赫兹）。
> 阶段化收益实质在装配层：类别独立开关（见 M3），引擎内不引入额外阶段复杂度。

- 规则按「优先级区间」天然分为固定阶段（10-19 dict、20-29 num、30+ post），阶段间输入/输出明确。
- 各阶段内部才按优先级叠加；阶段间通过 token 化天然隔离（M1 已实现）。
- 精度演进的后续选项是「逐阶段精确映射链」（替代 lengthChanged 弃用精配），
  因 V2.8 §7.5 已验证比例近似在单标记/单数字段有界，标为后续优化而非本轮必要。

### 4.3 场景回归语料库（对标 itntext reports）

- 新增 `internal/textnorm/corpus_test.go`：内嵌 `FULL_SCENARIO.csv` 风格语料：
  - 每条 = `(类别, 输入, 期望 effectiveText, 期望 DisplayText, 是否含 IndexMap)`。
  - 覆盖：标记、词典、数值、百分比、日期、金额、单位、**组合场景**（同段多类别叠加）。
- CI 每次跑全量，失败即回归阻断；比对输出逐字节 + IndexMap 覆盖断言。
- 保留既有黄金对比测试②（无数值类时与 `pronunciation.Apply` 逐字节一致）。

---

## 五、性能演进

### 5.1 现状优势（保留）

- Go + 纯内存、冻结后并发安全、每段 O(n) 单趟——远优于 itntext 的 Python+FST（两遍 + 排列枚举）。
- 词典命中即短路 + 数字快筛（`asciiDigitP`）合理。

### 5.2 优化点

- **修 O(n²)**：`expandDigits`/`quantityWords` 的 `[]rune` 转换 + 反复 `runeLen(sb.String())`
  （numbers.go:91,96）→ 改用「rune 切片 + 游标长度」，单趟线性。
- **快筛掩码**：`Engine` 加 `hasDigits/hasPercent/hasDate` 位掩码，`Run` 前单趟扫出类别命中，
  规则遍历只跑命中的类别（O(1) 掩码过滤，跳过未命中类）。
- **regexp 预编译**：所有 Pattern 的 `*regexp.Regexp` 在 `Build()` 冻结时编译并缓存，`Run` 零编译。
- **映射表复用**：`[]int` 用 `sync.Pool` 复用，降低 GC 压力。

---

## 六、迁移路径（V2.8 → V3.0）

> 目标：不破坏既有 API（`Engine.Run` / `StripMarkers` / `DefinitionRule` / `NumberRule`），
> 兼容既有调用方与测试，增量演进。

| 阶段 | 内容 | 兼容性 |
|---|---|---|
| M0（纯增量） | 抽出 `num2cn` 公共库（NumToCN/YearToCN）；修 O(n²)；快筛掩码（RegisterDigitRule/textMask） | 全兼容，测试不改 |
| M1 | `Pattern` 声明式入口 + token 化隔离（缺陷 B：5GHz 不再展开为五吉赫兹）；`DefinitionRule` 保留 | `DefinitionRule` 保留，TestDefinitionNumberCompose 断言随修复更新 |
| M2（并入 M1/M3） | 阶段化收益经评估落在装配层（类别开关）；lengthChanged 纪律保留（坐标对齐正确性保证，见 §4.2 实现注记） | IndexMap 语义保持 |
| M3 | 补 PPT 高频类别：percent → decimal → date → money → unit；独立开关 `PPTS_TEXT_NORM_CLASSES` | 默认关，零回退 |
| M4 | 场景回归语料库 `corpus_test.go` 全量接入 CI | 新增测试，既有用例保留 |
| M5 | 数据驱动：`contextual_rules` 表 + 运营后台管理界面（第二批） | 可回退（开关关闭即纯 embed） |

**落地顺序建议**：M0 → M1 为架构核心（对标 itntext 的 tagger→verbalizer + 类别隔离）；
M3 为价值增量（PPT 高频场景，含装配层类别开关）；M4 为质量保障；M5 为产品化（运营后台）。

---

## 七、验收判据

| 维度 | 判据 |
|---|---|
| 架构 | `Engine.Run` 内部走「token 化 → Pattern 流水线 → 精确映射」，无手写状态机新增类别 |
| 落地 | 百分比/小数/日期/金额/单位 各 ≥3 场景用例通过；组合场景（同段多类别）不吞字 |
| 易用性 | 新增「价格」规则 = 1 条 `RegisterPattern` 声明，无需手写扫描 |
| 可维护性 | `corpus_test.go` 全量回归 CI；`DefinitionRule`/`NumberRule` 旧接口仍可用 |
| 性能 | 长段（260s 级）`Run` 无 O(n²)；快筛掩码使无命中类别零遍历 |
| 兼容 | V2.8 全部既有测试不改即通过；生产 `PPTS_TEXT_NORM_NUMBERS=quantity` 行为不变（默认关新类别） |

---

## 八、明确不做的范围

- **不引入 Python/FST/sidecar**：itntext 的可复现性靠 pynini 编译，但引入 Python 会破坏
  当前纯 Go 单 worker 架构（正是 itntext 相对我们的代价）；用 Go `regexp` 自动机替代 FST 达成同层抽象。
- **不引入外部 ASR/对齐模型**：见 ADR-020（阶段二强制对齐暂缓）。
- **不改变 `pronunciation.Apply` 语义**：黄金对比测试②的范围约束仍有效（无数值类时逐字节一致）。

---

## 九、实现状态（2026-09-27）

### 9.1 已完成

| 阶段 | 落地内容 | 文件 |
|---|---|---|
| M0 | `NumToCN`/`YearToCN` 公共库；`expandDigits` 改 rune 游标修 O(n²)；快筛掩码 `textMask`/`RegisterDigitRule` | `numbers.go`/`engine.go` |
| M1 | `Pattern` 声明式入口（`RegisterPattern`/`patternRule`）；token 隔离修复缺陷 B（5GHz 不再展开为五吉赫兹） | `pattern.go`/`numbers.go` |
| M2 | 评估并入 M3：lengthChanged 纪律保留（坐标对齐正确性保证）；类别开关在装配层 | `app/textnorm.go` |
| M3 | 类别规则 `percent`/`decimal`/`date`/`money`/`unit` + 环境变量 `PPTS_TEXT_NORM_CLASSES`；`DecimalToCN` 公共库 | `classes.go`/`cmd/ppts/textnorm.go` |
| M4 | 场景回归语料库 `scenarioCorpus` + IndexMap 全覆盖断言 | `corpus_test.go` |
| M5 | 数据驱动 `contextual_rules`：`ReplacementPattern` 引擎入口、0048/0016 迁移、`internal/contextrule` 双实现、`/api/context-rules` CRUD + 运营后台页 `SettingsContextRules`；装配层 `TextNormContextRuleStore` 随引擎总开关加载 | `pattern.go`/`contextrule/*`/`internal/api/contextrule.go`/`web/...` |

全部测试（含 `-race`）通过；生产默认**零回退**（类别与数值默认不注册）。

### 9.2 实现注记（设计稿落地时的关键修正）

- **token 隔离空白名单语义**（§1.2.3）：白名单为空（原始 span 无任何独立数字，如 `支持 5GHz 频段`）
  必须表示「全部拦截」而非「全放行」——否则词典把 GHz→吉赫兹 后 number 会把 5 误展开。见 `independentWhitelist`。
- **RE2 无 lookahead → 优先级编排**：类别冲突（decimal 想吞 `6.3`，percent 想吞 `6.3%`，日期含小数分隔）
  用类别优先级（date=11 < percent=12 < money=13 < unit=14 < decimal=15）解决，全部先于 number(20)。
  `?`/`(?:)` 可用，`(?!...)`/`(?=...)` 不可用。
- **money 尾随「元」保留**：`13.5万元` 匹配到 `13.5万`，尾随 `元` 作为空隙恒等保留 → `人民币十三点五万元`。
- **等长改写不产生 IndexMap**：`GHz→吉赫兹`（3→3 rune）等长，属 V2.8 既有设计；IndexMap 仅承载字长变化。
- **并行工作流最小修复**：`internal/api/tenant.go` 审计记录 `rows := int64(0)`（原 `int` 与 `f.Rows int64` 类型不匹配），解除全仓编译阻塞；不改变该功能逻辑。
- **M5 数据建模**（§3.3）：`contextual_rules` 采用【单行=单规则】模型（非发音词典的 JSON 数组列），
  `tenant_id NULL` 即平台行（对标发音词典 `is_platform_default`，但不引入额外布尔列）。
  API 层 `validateContextRule` 拒绝非法正则（R1 不静默）；引擎装配 `ReplacementPattern` 编译失败同样报错。
  DB 规则 priority 建议 <20（先于数值展开）；与代码规则（词典 10/类别 11-15/数值 20）共享优先级空间，
  同 priority 时按注册顺序稳定排序。
- **route 门禁**：新增 `/api/context-rules` 已在 `route-inventory.json` 登记「keep」分组；
  `/debug/pprof/*` 5 条未登记属并行工作流（server.go:158-162）遗留，非本方案改动，未越界处理。
