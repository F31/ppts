# Tasks

> 交付纪律：本变更对应设计方案 §19 批次一的 G2/G4/G5（G1 只做契约、G6/G15 归 M2）。纯 Go、零新增依赖、零改动既有包。合并门禁：`go build ./...`、`go vet ./...`、`go test ./internal/design/...`，且 `git diff --stat` 不含 `internal/app/`、`internal/media/`、`web/`、`internal/animation/`。

## 1. 画布、栅格与令牌（PR1 · 已完成）

- [x] 1.1 `tokens.go`：`Canvas{1920,1080}`、`Grid{12,8,96,72,24}`、`Theme` 聚合结构与 `Default()`
- [x] 1.2 `Grid.Verify()` 用整数等式校验横纵两向，差额 MUST 为 0；`ColumnWidth()`/`RowHeight()` 由同一组参数推导，不独立声明
- [x] 1.3 字阶 8 档（`display/h1/h2/h3/lead/body/bodySm/caption`），含字号、行高倍数、字距、`Body`；`body`/`bodySm` ≥ 32px 的断言在 `Verify()` 中执行
- [x] 1.4 色板 10 项：`paper/paperAlt` + 6 个可读前景 + 语义色，`Readable=true` 的前景与所有 `paper` 组合必须 ≥ 4.5:1
- [x] 1.5 间距标尺（4 的倍数，严格递增）、圆角、描边
- [x] 1.6 单测 9 组：栅格等式、改坏 gutter/边距各 1 例、末格贴边、越界三例、字阶下限、字阶递减、缺正文档位、色板可读、不可读前景、未知用途、间距倍数、颜色解析

## 2. 版式原型（PR1 · 已完成）

- [x] 2.1 `archetypes.json`：10 个原型（`cover`/`section`/`bullets`/`split`/`full-bleed`/`big-number`/`quote`/`chart-focus`/`compare`/`closing`），每个含 `purpose` 与槽位
- [x] 2.2 `archetypes.go`：`//go:embed` + `ParseArchetypes` 自检——版本、id 唯一且为短横线小写、purpose 非空、槽位 1..12/1..8、同原型不重叠、至少一个文本槽位、主陈述槽位（title|quote|number）存在、role/align/vAlign 在允许集合内、未知 JSON 字段拒收
- [x] 2.3 `Slot.Rect` 等价实现为 `Grid.CellRect` + `Archetype.Rects`：越界返回错误而非裁剪
- [x] 2.4 单测 9 组：内置 10 个全部合法且落在画布内、id 集合、compare 双 body、越界（给出右边界 13）、重叠、缺主陈述、未知角色、非法 id/空 purpose、未知字段、数量不足

## 3. 运动质感（PR1 · 已完成）

- [x] 3.1 `motion.go`：`MotionProfile` + `DefaultMotion()`，数值对齐 §19.4（stagger 60–90ms、预备 15%/4–8px、回弹 damping 12–16 且幅度 ≤8%、hold ≥400ms、速度分层 600–900/400–600/240–400、节拍 3–5 个 × 900–1400ms）
- [x] 3.2 命名缓动 4 条：`standard`/`enter`/`exit`/`overshoot`，含控制点、CSS 表达式与 `At(t)`（先二分反解 x，再取 y）
- [x] 3.3 `StaggerOffsets(n, stepMs, mode)`：顺序与中心扩散，纯函数
- [x] 3.4 `Validate()`：区间左 ≤ 右、hold ≥ 400、回弹幅度 ≤ 0.2、大元素不短于小元素、必需分层与缓动齐全、x 控制点在 [0,1]
- [x] 3.5 单测：4 元素顺序 → `[0,75,150,225]`；中心扩散 n=1/3/4/5；端点与单调性；overshoot 峰值落在 (1, 1.08]；7 类坏参数

## 4. 观感度量（PR2 · 已完成）

- [x] 4.1 `metrics.go`：`PageFacts`、`Finding`、`Report`、`Report.OK()`、`BySeverity`
- [x] 4.2 规则 6 条：`P1` 正文字号（error）、`P2` 对比度（error）、`P3` 元素数（warn）、`P4` 留白（warn）、`P5` 节奏密度（>3 warn / >6 error）、`P6` 节拍数（warn）
- [x] 4.3 `ContrastRatio(fg,bg)`：sRGB 相对亮度 + gamma 反变换，保留两位小数
- [x] 4.4 单测 9 组：黑白 21、同色 1、对称、`#767676`≈4.54、干净页零发现、字号/对比度 error、4 类 warn 不阻断、密度硬上限、全未知跳过、短页跳过节拍、确定性
- [x] 4.5 纯函数断言：同一输入两次度量 `reflect.DeepEqual`

## 5. 资产清单契约（PR2 · 已完成）

- [x] 5.1 `assets.go`：`Asset{ID,Kind,Version,License,Attribution,File}` + `ParseAssets`（kind 白名单、version > 0、license 在允许集合、CC-BY 必须带 attribution、扩展名白名单、id 唯一）
- [x] 5.2 单测：合法清单 + 8 类拒绝（缺 license、未知 license、CC-BY 缺署名、重复 id、未知种类、扩展名、版本、清单版本）

## 6. 预览生成器（PR2 · 已完成）

- [x] 6.1 `cmd/design-gallery`：`-out` 指定输出（默认 `dist/design-gallery.html`）
- [x] 6.2 内容：10 个版式缩略图（16:9 + 槽位角色与跨度）、栅格示意图（列带/行带/安全区）、字阶样例（真实像素）、色板与实测对比度、四条缓动曲线（SVG 采样）、stagger 时序图、速度分层、度量阈值表
- [x] 6.3 自包含：内联 `<style>`，零 `http(s)://`、零 `<link>`、零 ES module、零 `fetch(`
- [x] 6.4 单测：生成物不含外部引用、覆盖全部原型 id 与缓动名与规则编号；`WriteGallery` 落到临时目录且体积合理

## 7. 验收门禁

- [x] 7.1 `go build ./... && go vet ./...` 通过
- [x] 7.2 `go test ./internal/design/...` 全绿（32 个用例）
- [x] 7.3 `go list -deps ./internal/design` 只含标准库
- [x] 7.4 `go mod tidy` 后 `go.mod`/`go.sum` 无变化（本变更不引入依赖）
- [x] 7.5 `git diff --stat` 确认 `internal/animation/`、`internal/app/`、`internal/media/`、`web/` 零改动
- [x] 7.6 生成预览页并用无头 Chrome 在 1280 宽下逐区截图核对：字阶、栅格、10 个版式、4 条曲线、错峰图、分层条、度量表全部正常（过程中修掉 4 处渲染缺陷，见 design.md）
- [x] 7.7 回写 design.md「实施记录」：数值落地、与 M1 catalog 的交集约束、4 处渲染缺陷的成因
