# 设计：动画渲染 MVP

## 1. 分层与依赖

```
internal/animation   (L1)  规格、catalog、校验、默认生成
        │  Revision / Preset / EffectProperties
        ▼
internal/render      (L3/L4 边界)  IR、编译器、IR 校验、hash、客户端、服务端 handler
        │  CompiledTimeline (JSON)
        ▼
cmd/renderer         (L4) 独立进程：HTTP + backend SPI
```

`internal/render` 直接导入 `internal/animation` 与 `internal/design`，MUST NOT 直接导入 `internal/app`、`internal/api`、`internal/media`、`internal/project`。`cmd/renderer` 只直接依赖 `internal/render`。这条依赖面用 `go list -f '{{.Imports}}'` 断言（直接导入），不用 `go list -deps`（传递闭包）。

**几何类型不借用 `project.Box`**：`render.EMUBox` 是本包自己的 4 字段结构。复用 `project.Box` 会把 `internal/project`（连带 `internal/db`、sqlite 驱动）链进渲染服务二进制——渲染容器不需要数据库，也不该背它的 CVE。

**已知的传递依赖（未修，已登记）**：`internal/animation` 的 `store_sqlite.go`、`store_postgres.go` 与模型同包，因此引入 `internal/db`、`pgx`、`tenant`；`cmd/renderer` 经 `animation` 传递链也会链接它们。根因是「模型与持久化实现同包」，正确修法是把两个 store 拆到 `internal/animation/animstore` 子包（M1 遗留结构，改动面约 600 行测试的类型限定）。本变更不动 M1 已验收代码，登记为 M3 前置项。

## 2. 关键决策

### D1 动效语义在编译期展开，IR 只留数值

设计方案 §8.3 的 `RenderPlan` 示例保留了 `"type": "slideIn"`。照做会让 Remotion 与 DOM 各写一遍 slideIn。本实现改为：

```json
{ "startMs": 0, "durationMs": 600,
  "easing": { "x1": 0.22, "y1": 1, "x2": 0.36, "y2": 1 },
  "props": [ { "name": "translateY", "from": 40, "to": 0 },
             { "name": "opacity", "from": 0, "to": 1 } ] }
```

`effect` 字段保留，只作审计与错误定位，规范明文禁止后端读它。

代价：新增动效要改 Go 代码（`plan.go` 的属性展开表），而不是只改 catalog JSON。这是**有意交换**——用一次编译期改动，换掉「两个后端必须同步实现」这个永久负担。为防止漏改，`plan_test.go` 断言 catalog 里每个 effect 都有展开，缺一个就红。

### D2 强度只缩放幅度

`intensity` 若同时缩放时长，会出现三个连锁问题：预览与规格不一致（用户看到 600ms，渲染出 400ms）；clip 可能被推出页尾（V3 已通过，编译期又越界）；节拍（3–5 clips/s）被静默改变。结论：**幅度归强度，时长归规格**。

`scale` 的处理要注意：`pop` 的 `scaleFrom=0.9` 表示「从 90% 放大到 100%」，其幅度是 `1-0.9=0.1`。强度应作用于 0.1 而不是 0.9，否则 `intensity=0` 会把元素缩成 10%。写成 `from = 1 - (1-scaleFrom)×intensity`。

### D3 页几何由调用方注入，缺省按高度 6858000 EMU 推断

`project.Document` 不含 `sldSz`（go-pptx 未暴露公开访问器），而 `Shape.Bounds` 是 EMU 绝对值。没有页宽高就无法换算像素。

取舍：`Geometry` 由调用方给；未给时按「PowerPoint 的标准页高恒为 6858000 EMU」推断宽度 = `6858000 × 底图宽高比`。16:9（12192000）与 4:3（9144000）都精确命中；自定义页尺寸会有比例误差，此时 IR 带 `geometry_inferred` 诊断。

**不给 `Bounds` 猜默认值**：宁可在 IR 里带一条「几何是推断的」，也不要让盒子静默错位——错位是看不见的，诊断是能被质量门禁抓到的。

### D4 页转场来自预设，不进可编辑规格

转场是页级属性、用户不会单独去调，放进 `Revision.Tracks` 会污染 V1–V12（转场既不是元素级也不是页面级动效，套现有规则必然要么放宽要么特判）。做法：`catalog.json` 的 preset 增加 `transition{type,durationMs}`，编辑器改预设时转场跟着走。

IR 里的 `transition` 挂在**后一页**（交叉发生在 `page[i-1] → page[i]`）。首屏 `none`。

### D4.5 速度分层只报"明显不在档"

视觉基线的分层（大 600–900 / 中 400–600 / 小 240–400ms）与 catalog 预设不同源：`business-calm` 的标题滑入是 700ms，在中元素档里超了 17%。按严格区间判定，几乎每一页都会带一条警告——真正的异常（图标 2000ms 浮动）被淹没，警告也就没人看了。

因此加 25% 相对容差，只报明显偏离的，并按页汇总成一条（不是每条 clip 一条）。**仍不改时长**：规格是用户能看见的值，编译器改时长会让预览与规格各说一套。

### D5 诊断不阻断，但必须留痕

视觉基线的 design.md §6.3 曾写「速度分层与 catalog 区间求交，交集为空则报错」。落地时改为**诊断**，理由是两级契约必须单调：

> 凡是 `Validate` 接受的规格，`Compile` 必须能产出 IR。

`bob`（1000–3000ms，用于图标）与小元素速度档（240–400ms）交集为空，而「图标浮动」是完全合法的产品诉求。若编译期硬失败，用户会看到「保存成功、导出失败」——这比观感偏差更难解释。所以：取 catalog 区间（用户可编辑的那一侧），记录 `duration_tier_conflict`，由质量门禁在成片统计里呈现。

### D6 渲染服务无状态 + backend SPI

服务本身只做：校验 → hash 去重 → 排队 → 调 backend → 落产物。job 表在内存（重启即丢，`planHash` 让我们能安全重跑）。产物只写 `workDir`，响应返回相对路径，由调用方取走。

backend 接口只有一个方法：

```go
Render(ctx, plan *Timeline, dir string, progress func(done, total int)) error
```

本变更只带 `stub` backend（按页时长写占位片段，供契约测试与端到端联通）。真实光栅后端（Remotion / Chromium）需要容器与 Node，随镜像单独落地，接口不变。

**为什么不在 Go 里写光栅后端**：位图区域动画的素材就是整页 PNG，任何「Go 自己画」的方案都要重新实现文本/矢量合成，等于重写解析器。Remotion 的边际成本更低，这就是 §8.1 的原判断。

### D7 客户端重试与降级

| 情况 | 行为 |
|---|---|
| 400/422 | 永久失败，返回 `*PlanError`（含字段路径与规则） |
| 401/403 | 永久失败（配置问题，不重试） |
| 408/429/5xx/超时 | 重试 2 次，指数退避 200ms→800ms（带 jitter） |
| 连接失败 / `/healthz` 不健康 | `ErrAnimationUnavailable` |

`ErrAnimationUnavailable` 是**降级信号**而不是错误：上层捕获后走静态导出并提示「动画服务暂不可用，已导出静态版本」（§8.8）。

## 3. IR 结构

```go
type Timeline struct {
    SchemaVersion string
    FPS, Width, Height int
    Background string
    Fonts []Font          // family + version，参与 hash
    Pages []Page
    Warnings []Diagnostic `json:",omitempty"` // 不参与 hash
}

type Page struct {
    Index int
    SlideID string
    DurationMs int64
    BaseImage string      // 相对 workDir，禁止绝对路径
    Layers []Layer
    Transition Transition
}

type Layer struct {
    ElementID, Kind string
    Box Box               // px
    AssetMode string      // bitmap-region | page-layer
    Clips []Motion
}

type Motion struct {
    ClipID, Effect string   // Effect 仅审计
    StartMs, DurationMs int64
    Easing Easing           // cubic-bezier 四点
    Props []Prop
}

type Prop struct{ Name string; From, To float64 }
```

### 3.1 hash 的规范化

`planHash = sha256(json.Marshal(canonical))`，其中 `canonical` 是去掉 `Warnings`、把 `Pages[*].BaseImage` 归一为 basename 后的副本。协议外加 `rendererVersion + fontVersion + fps + 画布`。

为什么不直接 hash 整个 JSON：`BaseImage` 含租户/项目前缀，同一份规格在不同项目下会算出不同 hash，跨项目缓存就失效了；而 `Warnings` 是诊断，不影响像素。

## 4. 校验分层

| 层 | 位置 | 失败后果 |
|---|---|---|
| 规格合法性 V1–V12 | `animation.Validate` | 400，用户可见规则编号 |
| IR 不变量 | `render.Validate` | 400，`invalid_plan` + 字段路径 |
| 几何/锚点/分段异常 | 编译器诊断 | 不失败，写入 `warnings` |

IR 不变量清单：`schemaVersion` 匹配；`fps ∈ [12,60]`；画布等于 Theme 画布；每页 `durationMs > 0`；`baseImage` 非空且不含 `..`/绝对路径；`box` 在画布内且宽高 ≥ 0；`startMs ≥ 0`、`durationMs ≥ 1`、`startMs+durationMs ≤ pageDurationMs + 1 帧`；属性名在白名单内；缓动四点 `x ∈ [0,1]`；转场类型/时长合规；页数 ≤ 500。

## 5. 风险

| 风险 | 缓解 |
|---|---|
| 没有真实光栅后端，M2 的「30s 样片」验收无法在本机完成 | 本变更验收口径改为「IR + 服务契约 + 客户端」全绿，样片验收挂到镜像落地；不把未验证的东西写成已完成 |
| 页几何靠推断，自定义页尺寸会偏 | `geometry_inferred` 诊断 + `Geometry` 显式覆盖；M3 从解析器补 `sldSz` 后删除推断分支 |
| `effect` 字段被后端误用，绕开 IR 语义 | IR 校验拒绝未知后端字段；code review 断言 + 规范明文；后续可加 `-tags strict` 下从 JSON 里剔除 |
| 单页 40 clip × 500 页导致 plan 过大 | 请求体上限 16MB，页数上限 500，超限 413 |
| 渲染服务二进制间接链接 pgx/sqlite | 已登记（§1）；镜像落地前拆分 `animstore` 子包。不影响正确性，只影响体积与漏洞面 |
| 分层警告退化为噪音 | 25% 容差 + 按页汇总（D4.5）；`TestPresetDurationsDoNotSpamTierWarnings` 用四套预设守住 |

## 6. 实施记录

**PR1（编译链）与 PR2（服务与客户端）已落地**，实现与设计的偏差：

| 项 | 设计 | 落地 | 原因 |
|---|---|---|---|
| 属性白名单 | opacity/translateX/translateY/scale | 增加 `scaleX`/`scaleY` | `grow` 的 `axis` 参数只能靠单轴缩放表达；用统一 `scale` 会把表格纵向拉长时横向也压扁 |
| 分层冲突 | 报错并指明 clip 与分层 | 25% 容差 + 按页汇总 + 不阻断 | 严格区间会让四套预设全部触发警告；硬失败会让"图标浮动"这种合法规格不可导出（两级契约必须单调） |
| `bob` 幅度 | catalog 未声明 offset | 常量 10px（1080p 基准）× 强度 | catalog 只有 `loop` 参数；10px 是"轻微浮动"的上限，再大就像元素在跳 |
| 页尺寸 | 由调用方提供 | 缺失时按标准页高 6858000 EMU + 底图宽高比推断 | `project.Document` 无 `sldSz`（go-pptx 未暴露）。推断会打 `geometry_inferred`，不做静默假设 |
| `grow` 强度 | 缩放幅度 | 不生效 | 生长的起止是 0.04→1，不存在"长多少"这个可调量；强行缩放会变成"从 30% 开始长"这种无意义效果 |
