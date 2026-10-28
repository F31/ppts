# 设计：动画视觉基线

## 1. 为什么现在做、为什么独立成包

设计方案 §19.8 的批次一包含 G1 资产、G2 版式、G4 运动质感、G5 度量、G6 交互预览、G15 基准。其中 G6/G15 必须有渲染器才有意义，G1 需要设计资源；真正**可以先落地、且必须先落地**的是 G2/G4/G5——因为它们是 M2 渲染器的输入，而不是输出。

因此本变更的定位是：**M2 的输入契约**。它不含绘制、不含编码、不含浏览器。

依赖方向：

```
internal/design  ← 标准库
       ↑
   M2 渲染服务 / M6 DOM 编译 / 未来的编辑器表单
```

`internal/design` MUST NOT import `internal/animation`、`internal/project`、`internal/app`。度量接口刻意接受纯数值（`PageFacts`）而不是 `animation.Revision`：否则"标准"就会被"实现"的形状绑死，将来规格结构一变，度量跟着改，门禁的历史数据也就不可比了。

## 2. 令牌为什么是 Go 常量，版式为什么是 JSON

| | 令牌 | 版式原型 |
|---|---|---|
| 形态 | `tokens.go` 中的结构体字面量 | `archetypes.json` + `//go:embed` |
| 理由 | 需要编译期类型与算术不变量（栅格等式、字阶单调），改坏要在 `go build`/`go test` 就失败 | 属于"内容表"，会随页面类型增补；JSON 便于评审与 diff，解析后做与 catalog 同风格的自检 |
| 校验时机 | 包初始化 + 单测 | `ParseArchetypes` 一次性全量自检 |

不用 codegen（与 M1 的 catalog 决策保持一致）：少一个生成步骤，就少一种"忘了重新生成"的漂移。

## 3. 关键决策

### D1 栅格用整数精确等式，不留浮点容差

1920 = 96×2 + 122×12 + 24×11；1080 = 72×2 + 96×8 + 24×7。

列宽 122、行高 96 都不是"好看的数字"，但它们是**唯一能满足等式的整数解**（在给定 margin/gutter 下）。渲染时若允许 0.5px 容差，元素边缘会落在半像素上，1080p 下表现为 1px 模糊描边。所以 `Verify()` 要求 `diff == 0`，不做四舍五入。

这也是为什么 margin/gutter 选 96/72/24：它们是 8 的倍数，且能整除出整数列宽。改这三个数必须同时满足等式，否则自检失败——约束写在代码里，而不是写在文档里靠人记。

### D2 对比度统一 4.5:1，不用 WCAG 的"大字号 3:1"豁免

WCAG 规定 ≥24px（或 ≥18.66px 加粗）属大文本，阈值 3:1。但本场景正文下限就是 32px，**若照搬该豁免，等于全部文本都降到 3:1**，规则自我失效。

结论：成片文本一律 4.5:1。这条比 WCAG 更严，是产品选择，必须写在 spec 里（已写入），否则将来会有人"按标准"把它改回 3:1。

### D3 严重级别二分：可读性 error、密度 warn

- **error（阻断）**：正文最小字号、文本对比度。这两条是"看不清"，没有商量空间。
- **warn（建议）**：元素数 7±2、留白比例、节奏密度（>3 clips/s）、节拍数 3–5。

理由：后四条是启发式，来自对参考片的归纳。把它们设成阻断，会逼编辑者为了过门禁而删内容——门禁应该指出风险，不该替代判断。唯一的例外是 clips/s > 6：到这一档已经不是"节奏快"，是"每个元素都在动"，直接判 error。

### D4 度量输入是事实，不是模型

`PageFacts` 只有七个数值：页时长、元素数、最小正文字号、最低文本对比度、占用面积比、clip 数、节拍数。调用方（M2 编译期、编辑器、离线检查脚本）各自从自己的模型里取值。

对比度进事实而不只进色板校验，是因为「声明可读」与「实际用对」是两件事：色板保证某组前景/背景通过 4.5:1，但页面上可能把 `inkMuted` 放在 `accent` 上，或叠了半透明遮罩。度量必须看到实际组合。

代价：调用方要写几行适配代码。收益：度量不随规格结构漂移，且可以被单测穷举——6 个数值的边界组合是有限的。

### D5 运动质感只出参数，不出实现

`MotionProfile` 提供 stagger 序列生成（顺序/中心扩散）与参数区间，**不提供**插值、不提供时间轴求解。理由：时序求解依赖具体元素与页面结构，属渲染器；基线只负责"间隔多少、预备多少、回弹多少、静置多久"这些可被评审的数值。

`speedTier` 与 M1 catalog 的 `durationRange` 会在 M2 编译期相遇。**交集为空时必须报错**，不许静默取其一——否则会出现"按基线该 600ms，按 catalog 只允许 240–400ms"这种互相打架却没人发现的局面。这条约束写在这里，由 M2 的变更落实。

### D6 预览页自包含

与 M6-1 同一条约束：`file://` 下 `fetch()` 与 ES module 会被 CORS 拦，外链字体会静默失败。所以预览页是**单个 HTML + 内联 `<style>` + 内联 `<script>`（IIFE）**，零外链。

预览页不追求好看，追求"能把数值摊开":每个版式画成 16:9 缩略图并标出槽位与所占列数/行数，色板旁直接标对比度实测值，字阶按实际比例排。任何人可以指着一格说"这个槽应该更宽"，然后改 JSON。

## 4. 数据形状

```go
type Theme struct {
    Canvas  Canvas   // 1920×1080
    Grid    Grid     // 12×8，marginX/marginY/gutter
    Type    []TypeToken // 命名档位，含字号/行高/字距/是否正文
    Space   []int    // 间距标尺（4 的倍数）
    Colors  []ColorToken // 含用途与可读契约
    Radius  []int
    Stroke  []int
}

type Archetype struct {
    ID      string
    Purpose string
    Slots   []Slot
}
type Slot struct {
    Role    string // title|subtitle|body|bullets|visual|chart|number|label|quote|attribution|meta|divider
    Col, ColSpan, Row, RowSpan int
    Align, VAlign string
}

type MotionProfile struct {
    Stagger      Range       // 60–90ms
    Anticipation struct{ Ratio, OffsetPx float64 } // 15%, 4–8px
    Overshoot    struct{ Damping Range; MaxAmp float64 } // 12–16, ≤8%
    HoldMs       int64       // ≥400
    SpeedTiers   map[string]Range // large 600–900 / medium 400–600 / small 240–400
    Beat         struct{ PerPage Range; UnitMs Range } // 3–5 个，900–1400ms
    Easings      []Easing    // 命名 cubic-bezier
}
```

## 5. 风险

| 风险 | 应对 |
|---|---|
| 令牌数值缺乏设计背书 | 每个数值带 §19.4 出处注释；预览页让非工程师可质疑；改动只发生在常量与 JSON，不牵动逻辑 |
| 10 个版式覆盖不了真实页面 | 原型是建议不是强制；M2 允许自由排版回退。本变更只保证原型自身合法 |
| 度量被当成 KPI 优化 | 只有两条 error，其余 warn；`Report` 保留实测值与阈值，便于回溯"为什么当时判过" |
| 预览页被当成成品 | 预览页只画槽位框与字阶，不含真实内容与动效；M2 的样片才是成品 |
| 与 `web/` 令牌重复 | 刻意不共享：控制台是 UI（字号 12–32px），成片是 1080p（字号 24–120px），共享只会互相迁就 |

## 6. 实施记录（PR1 + PR2 已落地）

### 6.1 与设计稿的偏差

| 项 | 设计稿 | 落地 | 原因 |
|---|---|---|---|
| 规则编号 | §19.6 未编号，我在 tasks 中初拟 `V-M1..V-M6` | `P1..P6` | `V` 前缀已被动画规格的 V1–V12 占用；同一编号体系出现两套含义，是后来最难查的混乱。`P` 取"观感（perception）" |
| `PageFacts` 字段 | 初拟 6 个 | 7 个（增加 `minTextContrast`） | 色板只保证"声明的组合"合法，页面仍可能把两种可读色叠错；度量必须看实际组合 |
| overshoot 控制点 | 初拟 `(0.34, 1.56, 0.64, 1)` | `(0.34, 1.42, 0.64, 1)` | 1.56 实测过冲 9.8%，超过 `MaxAmplitude = 8%`。曲线幅度与档案声明的上限必须一致，否则两处各说一套 |
| 校验顺序 | 先查数量下限 | 先逐项校验、后查数量 | 单项有错时，"哪个槽位越界"比"数量不足"有用 |

### 6.2 预览页的四处渲染缺陷（都是"看起来对、其实是错的"）

1. **16:9 用 `padding-top:56.25%` 被算歪**：百分数 padding 按**包含块宽度**解析，`.grid-slide` 受 `max-width:760px` 限制后高度仍按 1132px 算，ratio 变成 1.73 以下。改用 `aspect-ratio:16/9`。这类错误只有截图逐块量过才发现——测试断言不到 CSS。
2. **缓动曲线被等比缩放挤成一小段**：SVG 默认 `preserveAspectRatio` 在 100×148 的 viewBox 里按高度贴合，曲线只占卡片中间 70px。时序图不是几何图，加 `preserveAspectRatio="none"` 让横纵各自拉伸。
3. **错峰方块用 flex + margin-left 排布**：总宽超出面板后布局失效，方块逐个换行并被 `margin-left` 递进推成斜线。改为 `position:absolute` + 百分比 left（相对节奏不变、绝对尺寸不溢出）。
4. **中心扩散下镜像元素重合**：`[150,75,0,75,150]` 里 1&5、2&4 起止时刻相同，两个方块叠在一起，看起来像"少了一个元素"。改为按同偏移分组叠行显示——**这恰恰是最该被看见的信息：它们同时出现**。

### 6.3 与 M1 catalog 的交集约束（留给 M2）

`speedTier`（大 600–900 / 中 400–600 / 小 240–400ms）与 `animation.Catalog` 的 `durationRange` 会在 M2 编译期相遇。已知的潜在冲突点：`fadeIn` 的 `durationRange` 上限、`bob` 的下限 1000ms 与"小元素 240–400ms"根本不在同一区间。

M2 编译期 MUST 对每个 clip 做一次区间求交，`交集为空` 时**报错并指明 clip 与分层**，不许静默取其一。若确认要放宽某一侧，改的是 catalog 的区间并留注释，不是让编译器猜。

### 6.4 已验证

- `go test ./internal/design/...` 32 个用例全绿；`go vet` 干净；`go build ./...` 全仓通过。
- `go list -deps ./internal/design` 只有标准库（fmt / math / sort / strings / strconv / sync / embed / encoding-json / html / os / path）。
- 预览页 452KB 单文件，无任何外部引用；1280 宽下逐区截图核对通过（截图仅存于 `dist/`，已被 `.gitignore` 忽略）。
