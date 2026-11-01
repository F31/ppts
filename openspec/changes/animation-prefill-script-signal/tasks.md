# 任务

## 1. 分档读讲稿（Go）

- [x] 1.1 `internal/animation/bind/band.go`：`classifyBand` 接收 segments，判 `layoutOK && scriptOK`；
      新增 `SignalScriptStructure` / `SignalScriptFlat` 与 `BandReasonLayout/Script/Both`。
      签名改成返回 `bandVerdict`（分档 + 信号 + 方向覆盖 + 原因），四个返回值摊在调用处太容易串位
- [x] 1.2 `bind.go`：`Result` 增 `BandReason`；`Run` 把 segments 传进去；信号顺序固定为
      markers / clusters / directions / script-*
- [x] 1.3 `script_signals.go`：`scriptStructureKinds(segments)` 复用 `ordinalRe` / `detectCount` /
      `dirRe` / `tableRefs` / `imageRefs` / `centerWords`（与三条规则同口径，不另造词表）。
      数量词必须走 `detectCount`（2..12）而不是 `qtyMatch`——第一版用 `qtyMatch`，
      「毛利率比上季度高一个点」把「口语直讲」那页点亮成结构化，API 用例当场抓到
- [x] 1.4 单测：四格（有/无结构 × 有/无结构词）+ 结构词类别（序数/数量/空间/图表指代/
      「第二条」只算序数/「几个」不算/「一个点」不算）；`TestGoldenQ8` 加一条「五页都该结构化」，
      金标分类计数与 0.972/0.962 逐项不回归

## 2. 透出与呈现

- [x] 2.1 `internal/api/animation.go`：`animationPrefillDTO` 增 `BandReason`（omitempty）；
      `TestPrefillBandReadsNarration` 两格（讲稿讲了结构 → structured / 口语直讲 →
      low-confidence+script）走 HTTP 全链路
- [x] 2.2 `web/src/i18n/{zh,en}.json`：`editor.animationPrefillLowConfidence` 拆成
      `...Layout` / `...Script` / `...Both`，新增两条信号文案，`animationPrefillSignals` 的
      「版面信号」改为「结构信号」（它现在也列讲稿侧）；`i18n:check` 1369/1369 对等
- [x] 2.3 `web/src/AnimationEditor.tsx`：低置信提示按 `bandReason` 选文案
      （`prefillLowConfidenceKey`）；`web/src/animationPrefill.ts` 补两条信号 key
- [x] 2.4 `web/animation-check.mjs`：钉住「`script` 档文案必含『讲稿』、且不得复用
      『没有检测到结构化版面信号』」+「三种原因文案互不相同」+ 信号 key 映射

## 3. 验证

- [x] 3.1 `go build ./...`、`go vet ./...`、`go test -p 1 ./...` 全绿（44 个包）
- [x] 3.2 `npm run build`（contrast / api:check / route:check / core / polling / animation 53 用例 /
      tsc / vite）与 `npm run build:share` 通过；`npm run i18n:check` 1369/1369 对等
- [x] 3.3 用消融讲稿复跑：`segments-no-structure.json` 经 `bind.Run` 五页全为
      `low-confidence` + `script` + `script-flat`，召回仍是 24/36 = 0.667、精确 0.902
      ——分档只改呈现，没动一条绑定

## 4. 文档

- [x] 4.1 fidelity README「结构词消融」一节：缺陷状态改为已修，写清判据与「数量词走
      detectCount」的口径坑
- [ ] 4.2 `openspec/specs/animation-track-editing/spec.md` 分档条目随归档更新
