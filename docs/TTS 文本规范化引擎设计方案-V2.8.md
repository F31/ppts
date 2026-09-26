# TTS 文本规范化引擎设计方案 V2.8

> 版本：V2.8（2026-09-26，最终版）
> 上一版：V2.5（合集「唯一入口 Run + 三处评审修正」）
> 本文档是最终优化版，**取代 V2.5**。相对 V2.5 增量 = 两次独立评审的修正全部并入：
> 架构评审（2026-09-25）判据的落地（依赖不可用不退回成功、双实现漂移、负测） +
> 对 textnorm V2.5 的二次评审（词典异常空集、StepKey 非内容寻址连带风险、租户隔离显式验证、
> 安全前提、预置数据双路径、DisplayText 派生单实现、字符偏移映射）。
>
> 定位：在保留 V2.2「三条缝」与 V2.5「`internal/textnorm` 单包 + 唯一入口 `Run`」的同时，
> 把「可靠性信号」「持久化归属」「父源与派生文本边界」补齐到可验收程度。

---

## 〇、评审演进脉络与本文档的采纳清单

| 版次 | 内容 | 本文档继承 |
|---|---|---|
| V2 | 投机性抽象 | 裁掉多语言路由/interface 五件套/双公式/双并发 |
| V2.1 | 过度收敛 | 承认真实需求，恢复缝隙；记录「短路即保护」的隐藏漏洞 |
| V2.2 | 通用性优先 | 三条缝 + Options + 错误三档，作为蓝本 |
| V2.5 | 平台落地 + 首次评审修正 | 唯一入口 `Run`；Scanner 归引擎；`Build()` 冻结；黄金对比三支 |
| **V2.8** | **最终收敛** | 并入本表中「评审采纳」列的全部可靠性/安全/数据归属修正 |

### 本章直接采纳的评审意见（含出处）

| # | 评审来源 | 意见 | 本文档处置 |
|---|---|---|---|
| R1 | 架构审视 §8 判据三 | 词典「静默返回空集」故障真实存在（Phase 0.5 表格 pronunciation 行）；依赖不可用不能退回成功 | 词典适配层区分「正常空 vs 异常空」：加载错误→任务失败+指标；平台种子存在但合并结果为空→Warn+指标（§5.3） |
| R2 | 架构审视 §3.2 | StepKey 非内容寻址（scriptdraft.go:129）可能让「规则/词典变了但没重新合成」 | N1 补端到端用例：只改词典/规则、不改讲稿，断言新音频真实重新合成（§9） |
| R3 | 架构审视 §3.1（R4 教训） | 接口签名不约束则范围安全不可见；租户隔离要显式验证 | 适配器显式校验 tenantID，空/异常→报错；N1 补跨租户隔离负测（A 拿不到 B）(**§5.2/§9**) |
| R4 | 二次评审·安全 | tenant ID 缺失时不得默默用默认值；读音标记是内容审查的高优先级旁路 | 防御性 tenantID 校验；合规过滤（如将来引入）必须覆盖标记内部文本（§8） |
| R5 | 二次评审·预置数据 | 字面替换类走现有表+迁移；结构性规则 embed 代码，两者不混 | 双路径分离（§6） |
| R6 | 二次评审·DisplayText | 派生字段避免第二份实现；剥离逻辑单源；字幕不得含标记 | 剥离单实现 `StripMarkers`（与 Scanner 同语法）；字幕文本派生用同一函数（§7） |
| R7 | 二次评审·字符映射 | 逐字高亮若存在，须处理 effectiveText/DisplayText 字数不一致 | 已核实：`remapCharsToCount` 现有比例重映射覆盖；精确映射表标记为未来选项（§7.5） |
| R8 | 二次评审·存储 | 「派生」要明确写时算 vs 读时算；减少存储应取读时算 | DisplayText 读时派生、不落库（§7.3），并诚实说明列保留的兼容策略 |

---

## 一、ppts 现状审计补充（V2.8 数据依据）

### 1.1 已核实的代码事实

| 事实 | 证据 | 影响 |
|---|---|---|
| 字幕/播放逐字高亮**存在** | `web/src/Player.tsx:104-113`（`spokenCharAt` 消费 `cue.chars`）；`web/src/playerClock.ts:21-34` | 字符偏移映射必须处理，不能只给文本 |
| 字级时间戳已按比例重映射 | `internal/media/timeline.go:180-184` + `remapCharsToCount`（`t:230-280`） | effectiveText 字数≠DisplayText 字数时已有比例兜底（近似） |
| DEgrade display/spoken 为**两独立列** | `internal/narration/postgres.go:123-125`、`sqlite.go:116-118` | 两列并存现实存在，须定派生归属 |
| 前端当前 `displayText=spokenText=text` | `web/src/pages/ProjectEditor.tsx:1057`、`web/src/ScriptEditor.tsx:185-186` | 标记会原样进字幕（G1/G2 的字幕污染面） |
| 全仓**无内容合规过滤** | grep 敏感/违禁/合规/sensitive/banned/filter 无业务命中 | 读音标记的审查旁路风险当前无实害，但须留文档边界与验收钩子 |
| 发音词典加载错误被**静默** | `internal/app/narration.go:220-225`：`err==nil` 才用，否则空集 | R1 的落点：异常空与正常空不可区分 |
| 词典变更→effectiveText 变→configHash 变→合成缓存自动失效 | `narration.go:807 synthesisHash` 含 effectiveText；StepKey 也含 configHash（`narration.go:391`） | 引擎输出变化换缓存键的前提成立 |
| narration 的 StepKey **已是内容寻址** | `StepKey: "tts_segment:"+slide+":"+seg+":"+configHash`（`narration.go:391`） | R2 对 narration 路径风险低，但仍需 E2E 兜底（见 §9） |
| scriptdraft 的 StepKey **非内容寻址** | `pageStepKey` = `"page:v1:"+slideID`（`scriptdraft.go:129`） | 规则/词典对**讲稿生成**的影响不在本组件范围；本组件只保证配音链路 |

### 1.2 G 问题清单（V2.8 目标）

| # | 问题 | 处置 |
|---|---|---|
| G1 | `〔读：x〕` 前端插入、后端零处理，TTS 逐字朗读 `〔读：cháng〕` | Scanner 短路识别 → 读音文本送 TTS（§4.1） |
| G2 | `‖` 停顿标记后端零处理 | Scanner 短路 → `Pause{AfterRunes,DurationMS}` → `SpeechControl.Pauses`（§4.2） |
| G3 | 数字/型号无读法语义 | N2 可选注册（默认关） |
| G4 | 词典为字面量子串替换，无语言维度 | `definitionRule` + `Dictionary.Lookup(lang,key)`（§4.3） |
| G5 | 引擎需在 worker 并发下安全 | `Build()` 冻结 + 只读规则表 + RWMutex 词典（§3） |
| G6 | **词典加载错误静默降空（R1）** | 适配层区分正常空/异常空（§5.3） |
| G7 | **标记污染字幕（R6）** | DisplayText 读时派生 = `StripMarkers(SpokenText)`（§7） |

---

## 二、设计原则

| 原则 | 落地决策 |
|---|---|
| 轻量 | 无 interface 森林；规则一个函数签名；Scanner 归引擎；包内文件 ≤7 |
| 简洁 | 内置规则与用户规则同构注册；剥离语法只有一份实现 |
| 易用 | `textnorm.New(...).Build()` + `WithTextNorm(engine)`；`Run` 唯一入口 |
| 高效 | 线性扫描 + map 查表；无反射；`-race` 并发测试 |
| 通用 | 纯函数：`Run`/`StripMarkers` 整段→文本+停顿；零 ppts 依赖 |
| 可扩展 | 三条缝全部被内置代码使用（dogfooding） |
| 灵活 | Options 化：错误策略/停顿时长/语言/词典；数值规则默认关 |
| **可靠** | **词典异常必须可观测（R1）；`Build()` 冻结（并发地基）；PassThrough + A26** |
| **可落地** | N0-N3 里程碑 + 三支黄金测试 + E2E 规则变更用例 + 跨租户负测 |

---

## 三、核心机制（唯一入口 Run + 冻结 Engineering）

### 3.1 数据流

```
Segment.SpokenText（含任意位置标记）
  │ ① Scanner（引擎内部独占）：按显式定界符切 span，不切词
  ▼
[普通 span… | 读音 span（〔读：x〕） | 停顿 span（‖）]
  │ ② 普通 span → 规则表 Process；读音 span → 产出读音文本；停顿 span → Pause 事件
  ▼
③ Rejoin → Result{Text, Pauses}
```

### 3.2 公开 API

```go
package textnorm

type Pause struct{ AfterRunes int; DurationMS int }
type Result struct{ Text string; Pauses []Pause }

type Rule func(*Context) (string, bool)
type Context struct {
	Text     string     // 普通 span 完整文本
	Lang     string     // 语言（ppts: snapshot.Language）
	SpanKind SpanKind   // Scanner 标记的类别
	Raw      string     // 读音 span 内容，仅 SpanKind==Reading 有效
	shared   *sharedState
}

type Engine struct { rules []namedRule; scanner *spanScanner; frozen bool }
func (e *Engine) RegisterRule(name string, priority int, r Rule) // 冻结后 panic
func (e *Engine) Build()                                          // 冻结：此后 RegisterRule panic、Run 可并发
func (e *Engine) Run(text, lang string) Result                    // 唯一入口

// StripMarkers：剥离标记、保留可读内容的纯函数。与 Run 内部 Scanner 共用一个语法来源，
// 用于 DisplayText 派生（§7.3）——字幕侧不会存在第二份实现（R6）。
func StripMarkers(text string) string
```

### 3.3 并发安全（运行时保证）

- `Build()` 后 `RegisterRule` panic；`Run`/`StripMarkers` 只读 → goroutine-safe。
- worker 装配处强制 `Build()`，不 Build 返回错误（源头防运行期再注册）。

### 3.4 优先级区间

| 区间 | 层 | 说明 |
|---|---|---|
| 0–4 | 显式读音标记 | Scanner 短路；读音内容直送 TTS |
| 5–9 | 显式停顿标记 | Scanner 短路；产出 Pause 事件 |
| 10–19 | 定义权词典 | 普通 span 内子串替换（`Dictionary.Lookup(lang,key)`） |
| 20–29 | 数值分类策略（N2 可选） | 型号/年份/金额/数量，默认不注册 |
| 90–99 | 兜底启发式 | 数值分类后最小阻力比音节 |
| 100+ | 用户后置规则 | 项目特有补丁规则 |

---

## 四、内置规则

### 4.1 读音标记 `〔读：x〕`（Scanner 短路）

- `〔读：cháng〕` → 产出 `cháng`（送 TTS）；未闭合 → 不识别为标记，原样透传（不丢字）。
- 读音内容**结构性不进规则表**——任何词典/数值规则不可改写用户对 TTS 的直接裁决。
- **安全边界（R4）**：若未来引入内容合规过滤，必须在**剥离标记前**对 `〔读：…〕` **内部文本**也执行过滤；
  本条写入验收钩子（§9 N0 项），当前因全仓无过滤而无实害。

### 4.2 停顿标记 `‖`（Scanner 短路）

- `RTX5090‖性能强劲` → `Pause{AfterRunes:8, DurationMS:默认300}`，输出文本不含 `‖`。
- ppts 端映射 `SpeechControl.Pauses`（`tts.go:73`）；供应商不支持 → 结构化降级（不加停顿，不塞字符）。

### 4.3 定义权词典（priority 10-19）

```go
func definitionRule(dict Dictionary) Rule {
	return func(ctx *Context) (string, bool) {
		out, changed := applyDict(ctx.Text, dict.Lookup(ctx.Lang))
		return out, changed
	}
}
```

- 语义与今日 `pronunciation.Apply` 保持一致（逐条 ReplaceAll、enabled + 非空 pattern）；
  黄金对比测试保证逐字节可通过。
- **不内置切词器**：子串误伤（Model3 撞 3）沿用现状；需要精确边界时用读音标记直接裁决。
- **词典接口（三件套之一）**：

```go
// Dictionary 窄接口：查询是唯一契约；写路径由 MutableDictionary/现有 store 承担。
type Dictionary interface {
	Lookup(lang, key string) (string, bool)
}
```

---

## 五、词典适配层与可靠性信号（R1）

### 5.1 目标：区分「正常空」与「异常空」

- 正常空：租户确实未配置词典，透传即可，无告警。
- 异常空：词典**本应有内容**（平台种子存在/加载错误），遮羞为「空集」→ 必须可观测。

### 5.2 适配器签名（显式租户约束，R3）＋合并归属拍板

> **合并归属（评审拍板，动手前必须先定）**：租户自定义规则与平台种子（§6 路径一）的**合并只发生在 DictAdapter** 内部，
> **绝不改 `pronunciation.Store.LoadTenantDefault` 的语义**。
> - 依据：`LoadTenantDefault` 是**老路径**（`narration.go:220-225` 的 `pronunciation.Apply`）仍在使用的共享函数。
>   若把「回退平台种子」做进它，则种子上线会让**未接入 textnorm 的租户** effectiveText 也悄悄变化——
>   直接违反本方案「textnorm 关闭时零行为回退」承诺，且黄金对比测试②的「今日基准」会被种子污染。
> - 结论：`LoadTenantDefault(tenantID)` 永远只返回**租户自有规则**（现状不变，零兼容回归）；
>   平台种子由**新增的窄方法** `LoadPlatformDefault(ctx) (Rules, error)` 单独读取（PG/SQLite 双实现 + 往返测试），
>   DictAdapter 在内存里完成「租户行优先、平台种子兜底」的合并。爆炸半径锁定在**只有启用 textnorm 的调用方**。

```go
// app 层适配器：把 pronunciation.Store 的租户默认词典适配为 textnorm.Dictionary。
// tenantID 必须是 job 派生的可信值；空值直接报错，不默默回落默认（R4）。
func (a *DictAdapter) Lookup(lang, key string) (string, bool) {
	if a.tenantID == "" { panic // 或由构造函数返回错误 } 
	rules := a.rules.Load() // DictAdapter 内存合并：一次 LoadTenantDefault（租户）+ LoadPlatformDefault（平台种子）
	...
}

// 装配期：构造即加载并校验租户，构造失败 → narration job 失败，不静默。
func NewDictAdapter(store pronunciation.StoreLoader, tenantID, lang string, expectedSeedNonEmpty bool) (*DictAdapter, error)
```

> **`pronunciation.Store` 的接口增量仅为新增 `LoadPlatformDefault`**；`LoadTenantDefault` 签名与语义均不变，
> 老路径（`pronunciation.Apply`，未开 textnorm）继续读租户自有规则——种子上线对它透明。

### 5.3 异常空信号（三件套：开关/告警/产物留痕）

| 场景 | 处理 | 信号 |
|---|---|---|
| `LoadTenantDefault` 返回 error | 任务失败（不再静默空集） | 错误日志 + `ppts_textnorm_dict_load_error_total` |
| 无错误、空集、且**平台种子存在**（`expectedSeedNonEmpty`） | Warn + 继续（避免单点失败阻塞配音） | `ppts_textnorm_dict_empty_unexpected_total` + slog Warn |
| 无错误、空集、平台种子也空 | 静默透传（正常态） | 无 |
| 词典快照随每次 job 重建 | 内存适配，零持久化 | 无（构造不落库） |

> **说明**：这是架构审视 §8「依赖不可用永远不能退回成功」裁定的直接应用——异常空与正常空从此在行为与可观测性上可区分。

---

## 六、预置数据双路径（R5）

**原则：能复用现有持久化就复用；不该进数据库的留在代码。两者不混。**

### 路径一：字面替换类词条（型号读音、专有名词 → 现有表 + 迁移种子）

- 现表 `pronunciation_dictionaries`（PG+SQLite 双实现且已有往返测试与列错位保护）。
- **平台种子行的数据建模（拍板，写迁移前必须先定）**：
  - 加独立布尔列 `is_platform_default`（`NOT NULL DEFAULT false`），**不用哨兵字符串** `tenant_id='__platform__'`。
    理由（评审确认）：本项目靠 `tenant.Run` 设置的 `app.tenant_id` 会话变量走 RLS 隔离（架构审视反复确认的机制）；
    往 `tenant_id` 塞非真实租户的哨兵串，要么在 RLS 策略下对真实租户不可见（种子形同虚设），要么需为哨兵值
    单独写 RLS 例外分支（新增需测试覆盖的攻击面，且手工命名空间有撞真实租户 ID 的隐患）。
  - 取列与判断策略：若 `tenant_id IS NULL` 视为平台级数据、同时 `is_platform_default=true`；真实租户行
    `tenant_id` 为真实值、`is_platform_default=false`。查询与 RLS 策略按「租户行 / 平台默认行」两类**分开写条件**，
    不共用一个字符串空间。
- **读取与合并（与 §5.2 合并归属一致，这里只补充 Store 侧形态）**：
  - `LoadTenantDefault(tenantID)` **语义保持现状**（只读租户自有行，对平台种子零感知）。
  - 新增 `LoadPlatformDefault(ctx) (Rules, error)`（PG/SQLite 双侧实现 + 往返测试），只查 `is_platform_default=true` 行。
  - DictAdapter 在内存合并：**租户行优先，缺失时回退平台种子**（合并逻辑单点，仅 textnorm 路径可见）。
- **双实现与测试纪律**：新列同步进 PG/SQLite 两侧 schema（迁移文件各写一份）+ 已有 SQLite 往返测试模板
  （不回只改 PG 一侧，防列错位回归）。
- 管理员加词条走现有词典管理 API；本组件不另做配置界面。平台种子行不允许出现在词典管理列表
  （列表查询过滤 `is_platform_default=false`），避免管理员误改/误删平台级数据。

### 路径二：结构性规则数据（品牌代际序列、数值阈值 → go:embed，不进 DB）

- `internal/textnorm/data/brand_history.json`（如 `brandHistory["mate"]={Gens:[...],AdjacentReadAsInt:true}`）；
  `//go:embed` 编入二进制。
- 属「规则函数的参数」，是代码逻辑一部分，**默认不启用（N2 可选）**；修改须走代码 review，
  对「数值语义可能与讲稿一致性冲突」形成把关。
- 不混入 `pronunciation_dictionaries`：避免「运营可改代码逻辑数据」与「字面替换」两类语义同表。

---

## 七、DisplayText 派生与剥离单实现（R6/R7/R8）

### 7.1 现状（已核实）

- `narration_segments` 有 `display_text` 与 `spoken_text` 两列；前端写同值；
  标记存在于两者 → 字幕（`timeline.go:187` 用 `DisplayText`）与 TTS 输入（`narration.go:379`）均含标记。

### 7.2 目标模型：SpokenText 为单一权威源，DisplayText 读时派生

```
存储侧：仅 SpokenText 为可编辑事实源（可含标记，编辑器照常显示/编辑标记）。
读侧：DisplayText = StripMarkers(SpokenText)   —— 字幕/导出/播放统一用派生值。
```

- 编辑器：继续显示并编辑含标记的文本（用户必须能看见标记才能维护读音/停顿）——不削减可读写。
- 字幕/导出：使用 `StripMarkers(SpokenText)` 干净文本——标记零污染（R6）。
- **存储收益（R8 诚实口径）**：`display_text` 列转为**兼容占位**（不再写独立用户副本，或写 `StripMarkers(spoken)`），
  彻底消除「改了 SpokenText 忘同步 DisplayText」的陈旧数据风险。是否物理删列属后续迁移项（可选的 N4）；
  在删列前，读路径一律以派生值为准，不读列上的旧值。

### 7.3 单实现（R6：不允许第二份剥离逻辑）

- `textnorm.StripMarkers(text)` 与 `Run` 内部 Scanner **共用同一份 span 语法实现**
  （Scanner 的一个模式：只产出普通/读音/停顿三类 span，剥离停顿、展开读音、拼接普通）。
- 字幕派生点（`internal/app/narration.go` 构建 timeline 时）调用 `StripMarkers`；
  **引擎与字幕两端永远走同一个函数**，不存在「今天一致、某天漂移」的第二份正则。

### 7.4 验收断言（把边界钉死成测试，而非靠假设）

- N1 测试：字幕文本（`SubtitleCue.Text`）不含 `〔`、`〕`、`‖`。
- 编辑器可见性不受影响（前端渲染仍用含标记文本渲染输入框）。

### 7.5 语音↔高亮同步（§7.5-7.6 精细化为专项，2026-09-26）

同步问题分**两个时间域**，必须分开论证——只做「文本→文本映射」不算考虑同步：

**时间域 A：effectiveText ↔ DisplayText 的字符序号映射（文本域）**
- 逐字高亮存在：`Player.tsx → cue.Chars → remapCharsToCount`。
- 现状：`remapCharsToCount`（`timeline.go:233`）把 effectiveText 字级 token **整段均匀比例重映射**为
  DisplayText 字数。**局限（已核实）**：它是线性插值，不携带"某字对应音频哪个区间"的局部信息；
  当词典/数值（N2）替换造成字对不齐时，高亮逐字系统性漂移。
- **V2.8 裁量**：读音/停顿标记经"展开读音、剥离停顿"后与 DisplayText 字数天然一致（不触发重映射），
  默认（标记+词典）链路不劣化现状；**N2 数值展开（`2024→二零二四`）才触发**，届时按 A 栏下述接口映射。
- **映射缝（现在定义接口，N2 可选实现，避免事后重构）**：

```go
// IndexMap 描述受规范化影响字符的 原文本位置→派生文本位置 对应。
// 引擎 Run/StripMarkers 在处理"字对不齐"规则时可选产出；默认 nil = 字数一致。
type IndexMap struct {
	Segments []IndexSeg // 原文本 [SrcStart,SrcEnd) → 派生文本 [DstStart,DstEnd)
}
type IndexSeg struct{ SrcStart, SrcEnd, DstStart, DstEnd int }
```

ppts 的 `SubtitleCue.Chars` 装配时：若 `IndexMap != nil` 用它做逐字定位（精确），否则落回
`remapCharsToCount`（比例近似）——**两条路径显式并存，接口先定，近似路径是回退而非唯一方案**。

**时间域 B：语音时间轴 ↔ 高亮时间轴（音频域）**
- **停顿自修正的论证（本版补写）**：`buildEstimatedVADAlignment` 从**实际 wav** 检测静音锚点
  （`vad_align.go:55-72`，`detectSilenceRuns` → `distributeByAnchors/Onsets`）。因此对**支持停顿的供应商**，
  `SpeechControl.Pauses` 会在音频留下真实静音 → VAD 自动把停顿后字符重新锚定到语音恢复点 → 高亮时序自修正，
  无需显式偏移补偿。这是本设计在时间域 B 的**结构性安全面**，必须写入文档作为论证依据。
- **供应商不消费 Pauses 的处置（A26 化）**：已核实 siliconflow 的 payload 只带
  `model/input/voice/format/speed/sample_rate`（`siliconflow.go:72-83`），**不消费 `SpeechControl.Pauses`**。
  此时 `‖` 不得静默消失：装配层需按 `Capabilities`（或按供应商实现）判定停顿支持，
  不支持则**降级为"产出一致但停顿不生效"并在 `SynthesisResult.Warnings` 回显**
  （如 `pause_not_supported`），前端据此提示「当前音色不支持停顿标记」——符合 A26「不假成功」。

**验收断言（§9 N1/N3 落地）**
1. N1：停顿标记在支持/不支持两种供应商下，audio 与高亮**时序自洽**（前者有真实静音、后者无但一致连续）。
2. N3：含 N2 数值展开的分段，`IndexMap` 存在时每字高亮位置与语音边界误差 ≤ 阈值（测试语料标定）；
   无 `IndexMap` 时回落比例近似并文档标注近似。
3. N3：不消费 Pauses 的供应商，`Warnings` 至少出现一次停顿不支持信号。

---

## 八、安全前提与防御（R4）

| 项 | 处置 |
|---|---|
| tenantID 可信 | 引擎/适配器以 job 派生 tenantID 为准；空/异常 → 构造报错，不默默回落默认 |
| 租户隔离 | R3：适配器显式校验 + N1 跨租户负测（A 租户拿不到 B 租户词条） |
| 读音标记旁路审查 | 当前无内容合规过滤（已核实），无实害；若未来引入，须先于标记剥离对标记内部文本执行过滤；写验收钩子（§9） |
| 注入面 | 引擎纯内存、无持久化、无外部 I/O、冻结后不可变 → 攻击面最小；不引入新权限模型 |

---

## 九、验收矩阵与里程碑

| 里程碑 | 内容 | 验收标准 |
|---|---|---|
| **N0 骨架** | textnorm（Scanner/Engine/Run/StripMarkers + 读/停标记 + Options + Build 冻结）+ 单测 | `go test -race ./internal/textnorm/...` 绿；负测：未闭合读标记不丢字 / Build 后 RegisterRule panic / 停顿 AfterRunes / `StripMarkers("〔读：a〕b‖c")=="ab c 不含‖与〔〕"` |
| **N1 接入** | narration.go:379 `Run` 插缝 + `WithTextNorm`/`NewTextNormEngine`/`DictAdapter` + worker 装配 + 停顿→SpeechControl + DisplayText 派生 + **R1 信号（词典异常）** + **同步：停顿在 支持/不支持 供应商两态下时序自洽 + 停顿不支持回显 Warning** | `go test ./internal/app/...` 绿；额外断言 ① 字幕无标记语法 ② 停顿进 SpeechControl ③ 缓存键随停顿变化 ④ **E2E：只改词典/规则、不改讲稿 → 断言新音频真实重新合成（provider 调用次数 +1）** ⑤ **跨租户负测：A 租户词典不出现于 B** ⑥ **词典加载 error → 任务失败而非静默空集** ⑦ **不消费 Pauses 的供应商 → Warnings 含停顿不支持信号** |
| **N2 数值可选** | 型号/品牌/年份读法走 `RegisterRule`（默认关）+ embed 代际 JSON；提示词与 validation 对齐 | `alignment_bench_test.go` 语料复验 max<500ms/CV<0.6 不变；开启时黄金对比②仅数值差 |
| **N3 硬化** | 黄金对比三支（引用于此）+ 并发压测 + Options 全组合无 panic/无 data race + 移除过渡 `pronunciation.Apply` + **同步专项：N2 数值展开后 IndexMap 精确映射 vs remapCharsToCount 近似回退双路径验证 + 时序一致容差** | 三支全绿；新增 R1 指标可观测性测试；IndexMap 存在时高亮-语音边界误差 ≤ 阈值 |
| N4（可选） | 物理删除 `display_text` 列迁移 | 读路径全派生后执行；回归 narration 往返测试 |

### 黄金对比测试三支（N3，逐字节）

| 分支 | 语料 | 断言 |
|---|---|---|
| ① 空规则引擎 | 任意文本 | `Run` 输出 == 输入（零副作用） |
| ② 默认引擎·无标记文本 | 纯讲稿段落 | `Run` 输出 == 今日 `pronunciation.Apply(spoken, dict)` 输出（逐字节） |
| ③ 默认引擎·含标记文本 | 同段+标记 | 输出 == ② 基础上仅剥离定界符；停顿 AfterRunes 与偏移一致 |

---

## 十、与总体架构评审的衔接

- 不新增存储/双实现/proto；示范「app 窄能力注入 + 收口」。
- 本组件的 **R1/R3/R6 落实**可直接作为 Phase 1「依赖不可用不退回成功」「负测门禁」的样板。
- 优先级：**N0+N1（读音/停顿 + 字幕零污染 + 词典异常告警）约 2 天闭环。**

### 期望管理（验收/演示时必读，评审补记）

G1 与 G2 的「闭环程度」不同，**对外同步与演示时必须分两层说清，避免"没做完"的错觉**：

| 功能 | N1 上线后 | 说明 |
|---|---|---|
| G1 读音标记（〔读：x〕） | **完全闭环** | 标记不再被 TTS 逐字朗读（`读：cháng〕` 不会再被念出来），读音文本正确送 TTS |
| G2 停顿标记（‖）· 误读修复 | **闭环** | `‖` 从 effectiveText 正确剥离，**不会再被念成字/词**——这是实打实的修复 |
| G2 停顿标记（‖）· 停顿功能本身 | **暂未闭环，依赖供应商** | 已核实当前唯一生产供应商 siliconflow 的请求体不传 `SpeechControl.Pauses`（`siliconflow.go:72-83`），故「真的停顿一下」目前不会发生，只在 `Warnings` 回显 `pause_not_supported` 供前端提示。待接入支持停顿的供应商（或在 siliconflow 适配器落地停顿实现）后该层才闭合 |

> **演示口径**：「方言/专名读音现在能指定了」「停顿标记不再被误读为文字」是 N1 的真实成果；
> 「‖ 真的停顿」请在 Warnings 提示出现时如实说明「当前音色暂不支持停顿，已在提示中明示」，而不是含糊其辞。

---

## 十一、明确不做（诚实边界）

- LaTeX/MathML 解析器：等 OMML 真实需求。
- 语言检测 / 多语言路由：不做；`Context.Lang` 保留。
- 开放域多音字消歧：用 `〔读：x〕` 注音；Scanner 结构性短路。
- 数值语义默认改写：避免与讲稿一致性冲突（N2 仅可选）。
- 精确字符映射表：等逐字精确高亮需求（§7.5）。
- 内容合规过滤：当前无实害；仅留文档边界与验收钩子（§8）。

---

## 十二、复杂度对照与结论

| 维度 | V2.2 | V2.5 | **V2.8** |
|---|---|---|---|
| 顶层调用 | `Normalize`+`New(opts)` | `textnorm.New(...).Build()`+`Run` | 同左 + `StripMarkers` 导出 |
| 扫描/切分 | 无 | Scanner 归引擎 | 同左；剥离语法单源（字幕共用） |
| 保护机制 | 命中短路+占位符 | 结构性 span 短路+PassThrough | 同左 |
| 词典 | 窄接口+RWMutex | 复用 Rules 适配 | **+异常空信号（R1）+显式租户校验（R3）** |
| 预置数据 | embed 词表 | — | **双路径：迁移种子（字面）+embed（结构性）** |
| DisplayText | — | 只动 effectiveText | **读时派生 `StripMarkers`，字幕零污染（R6）** |
| 可靠性 | 三档 FailPolicy | PassThrough 默认 | **错误不再静默降空；变更必须重新合成（E2E+负测）** |
| 包数/存储 | 6/0 | 1 包+1 缝 | 1 包+1 缝；可选 N4 删列 |

**结论**：V2.8 在 V2.5「正确插缝」基础上，真正落地了架构审视反复强调的三条判据——
① 字典异常空不再与「没配词典」行为同形（R1）；② 注入双实现漂移风险在字幕侧通过 `StripMarkers` 单源消除（R6）；
③ 租户隔离与"规则/词典变更真重合成"变成显式负测，而不是靠假设（R3/R2）。
这是「轻量、简洁、易用、高效」与「可靠、可扩展、可落地」在 ppts 的最终平衡点。