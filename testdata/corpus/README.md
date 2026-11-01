# ppts 兼容性语料库

目的：文档适配器（`internal/project` 读路径）与渲染链路的回归护栏。
`go test -tags=corpus ./...` 回放本清单；源文件缺席的项目以 `Skipf` 跳过，不误报红。

规模（V4.0 §15.3：至少 30 份、100+ 页的组合语料）：

| 类别 | 份数 | 页数 |
|---|---|---|
| 本仓库自产（`generated/s1*.pptx`） | 30 | 219 |
| 外部登记（`externalPath`，不入库） | 3 | 0（未知） |

份数门槛只数自产条目：外置金样能否检出取决于开发者机器，拿它凑数会让门槛
在一台机器上成立、在 CI 上不成立。

## 条目来源与许可

| 目录 | 来源 | 许可/可再分发 |
|---|---|---|
| `generated/s1*.pptx` | 本仓库 `scripts/gen_corpus` 用 go-pptx 生成 | 可再分发（本项目自产） |
| s001-text / s002-table / s003-image | go-pptx 仓库公开金样（LibreOffice 生成） | 可再分发（go-pptx 项目公开样本） |
| ext-*（外置，不落本仓库） | go-pptx 私有登记样本，仅 manifest 索引 | 私有，不入库 |

## 覆盖矩阵

每类至少一份，且都带说明性备注（备注是讲稿的源材料）。

| 维度 | 条目 | 生成方式 |
|---|---|---|
| 纯文本 / 多页 / 长文档 | s100–s112、s129（50 页） | 文本框 + 备注 |
| 表格 | s113、s124、s122 | `Slide.AddTable`（3×4，含中英混排与数字） |
| 图表 | s114（柱/折/饼各一页）、s125、s122 | `Slide.AddChart` |
| 图片 | s115、s126、s122 | `Slide.AddPicture` + 程序生成的渐变 PNG |
| 嵌入音频 + `p:timing` | s116、s127 | `Slide.UpsertNarration` |
| 切页过渡 / 自动换页 | s117 | `SetTransition` / `SetAdvanceAfter` |
| 中英混读 / 数字实体 | s119、s128 | 多段落混排（型号、单位、日期） |
| 字体样式 | s120 | `AddParagraph` + `AddRun` + 显式 `FontStyle` |
| 长段落溢出 | s121 | 约 800 字单框 |
| 空页 | s118 第 2 页 | 不放任何形状（对应报告"既无文字也无备注"） |
| 隐藏页 | s123 第 2 页 | 生成后重写 `presentation.xml` 的 `p:sldId@show="0"` |

**组形状没有合成样本**：go-pptx 当前只提供 `GroupShape` 读侧（`Children()`），没有建组 API。
这一维只靠外置金样覆盖，不在合成语料里伪造——伪造一份"看起来像组"的 XML 会让读路径
的用例检出的是一份不符合 OOXML 结构的文件。

## 期望特性（`features` 字段）

`manifest.json` 的每个条目可以登记 `features`（`table` / `chart` / `picture` / `audio` /
`timing` / `transition` / `auto_advance` / `hidden`），回放测试会断言这些标记**真的在读路径
上出现**。

这一列是语料真正的护栏：只断言"文件能解析"的话，生成器写出十份表格而读路径全认成
文本框，测试照样绿。登记期望后，"文件里有"与"读出来有"才被绑在一起。

同理，`hiddenPages` 显式写出期望隐藏页数（0 也要写），否则"没隐藏页"与"漏读隐藏页"
无法区分。

## 维护命令

```bash
go run ./scripts/gen_corpus          # 重建 generated/（先删后写，Save 拒绝覆盖）
go test -tags=corpus ./internal/...  # 回放注册语料
```

生成器自身带两道门槛：份数 ≥ 30、总页数 ≥ 100（G2-6 评测集要求）。

## 新增条目规则

1. 合成样本：在 `scripts/gen_corpus` 的 `decks` 里加一行，`kind` 决定每页放什么；
   同时在 `manifest.json` 登记 `pages`、`withNotes`、`features`（有隐藏页再加 `hiddenPages`）。
2. 外部样本：在 `manifest.json` 登记 `externalPath`（相对 `externalCorpusRoot`），文件不复制入库。
3. 登记 `features` 前先实测：`features` 是断言，不是愿望清单。写了读不出来，CI 就红——
   那是好事，说明覆盖没生效。
4. AI 评测集（G2-6）另立 `testdata/ai-eval/`，见开发计划 §4.3。
