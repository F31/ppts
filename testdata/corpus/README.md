# ppts 兼容性语料库（G0-6 首建）

目的：文档适配器（`internal/project` 读路径）与渲染链路的回归护栏。
`go test -tags=corpus ./...` 回放本清单；源文件缺席的项目以 `Skipf` 跳过，不误报红。

## 条目来源与许可

| 目录 | 来源 | 许可/可再分发 |
|---|---|---|
| `generated/s1*.pptx` | 本仓库 `scripts/gen_corpus` 用 go-pptx 生成（文本/备注/多页） | 可再分发（本项目自产） |
| s001-text / s002-table / s003-image | go-pptx 仓库公开金样（LibreOffice 生成，`testdata/corpus/`） | 可再分发（go-pptx 项目公开样本） |
| ext-*（外置，不落本仓库） | go-pptx 私有登记样本，仅 manifest 索引 | 私有，不入库 |

## 维护命令

```bash
go run ./scripts/gen_corpus          # 重新生成自带合成语料（覆盖 generated/）
go test -tags=corpus ./internal/...  # 回放注册语料
```

## 新增条目规则

1. 合成样本：扩展 `scripts/gen_corpus`，覆盖新特性（表格/图表/隐藏页/动画特征等）。
2. 外部样本：在 `manifest.json` 登记 `externalPath`（相对 `externalCorpusRoot`），文件不复制入库。
3. 每个条目登记预期 `pages`（0 = 未知）与 `withNotes`（是否应有备注页）。
4. AI 评测集（G2-6）另立 `testdata/ai-eval/`，见开发计划 §4.3。