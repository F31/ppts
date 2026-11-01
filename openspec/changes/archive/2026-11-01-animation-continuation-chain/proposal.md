# 提案：链式多页延续（>2 段）

## 为什么做

`animation-cross-scene-continuation`（G8）的 MVP 只支持「跨相邻两页一次」。它的 OUT 里明确把
**链式跨多页**留给后续 change，`design.md` 也写了「次页段若其 Track 又标 `continues` 则递归再拆
（本实现未做，余量段 `Continues=false`）」。

现状的真实行为（已核实 `internal/render/compile.go`）：
- `injectContinuations` 只往 `i+1` 页注入一次延续段，不检查该段是否在次页内仍没演完；
- `validateMotion` 曾要求延续段的 `startMs + durationMs` 落在页内——链式段必然违反，所以即使注入了也会被
  `Plan.Validate` 拒掉；
- `seam-gate`（V19）的配对规则只认「前一页的非延续源段」，链上的第 2 跳会被判成「未找到源段」。

结果：一个 15 秒的进度条跨 3 页时，第 3 页会把元素画成**从中间态定格**（第 2 页注入的段在页尾被片长
裁掉），表现为「跨到第三页时元素突然停住」——不报错、不崩，只是看起来坏了。

## 做什么

- **编译器**：`injectContinuations` 改为逐页循环注入。每注入一页，若 `offset + 页长 < duration`，
  就把 `offset` 加上该页页长、以新的负起点注入下一页，直到演完或没有下一页。
- **校验**：延续段（`isContinuation`）与源段（`continuationSource`）一样豁免「超页时长」检查——
  它们的余量由下一跳或末页片长承接，是设计要求而非规格错误。
- **链尾诊断**：到达末页仍有余量时记 `continuation_target_missing`（余量被片长裁掉，不阻断）；
  链中某页缺少同 `elementId` 层时，停止后续注入并记录诊断——**已注入的段仍有效**。
- **门禁**：`seam-gate` 的配对规则推广为「本段 offset = 上一段在页尾处的 clip 时间」，
  源段与上一跳延续段用同一判据（`prevOffset + 页长` 与 `页长 - start` 是同一件事）。
- **夹具**：`share-baseline -continuation` 在 ≥3 页时额外注入一条跨三页的链（`chain-0`），
  duration 取 `offset + 2 页 - 1s`，正好在第 3 页内演完。

## 不做什么

- 不引入全局时间轴（路线 A）：每页仍是独立段，只是同一个 clip 多切了几刀。
- 不做「链上元素静态位置/样式跨页联动」：仍要求各页同元素静态视觉一致（作者责任，MVP 约定）。
- 不改单跳语义：`offset` 的推导与负起点公式完全不变，链式只是把它重复 N 次。
