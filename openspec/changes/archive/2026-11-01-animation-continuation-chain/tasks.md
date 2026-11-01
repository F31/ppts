# 任务清单

> 一个里程碑 = 一次 commit + push + 全量验证。

## 1. 实现

- [x] 1.1 `internal/render/compile.go`：`injectContinuations` 改逐页循环；每跳 `offset += 上一页页长`、
  `StartMs = -offset`；1 帧容差与 `compileClip` 同口径；段按值复制避免首跳被覆盖
- [x] 1.2 链尾与链中断诊断：末页仍有余量 → `continuation_target_missing`（余量被片长裁掉，不阻断）；
  链中某页缺同 `elementId` 层 → 停止后续注入，已注入段保留
- [x] 1.3 `internal/render/validate.go`：延续段（`IsContinuation`）与源段一样豁免「超页时长」检查；
  非延续 clip 的负起点仍非法
- [x] 1.4 `scripts/review/seam-gate.mjs`：配对判据推广为 expectedOffset（源段 `页长-start` /
  上一跳 `offset+页长`），V19.5 对每一跳单独执行
- [x] 1.5 `cmd/share-baseline`：`-continuation` 在 ≥3 页时注入跨三页的 `chain-0` 夹具

## 2. 规格

- [x] 2.1 `render-plan` MODIFIED「跨页延续在编译期拆分」：从「两段」改为「覆盖整个 clip 的 N 段」，
  写明第 k 跳 offset 公式与链中断/链尾行为；保留全部既有场景
- [x] 2.2 路线图补一行，写明链式与「跨页静态位置联动」仍是两个独立能力

## 3. 验证

- [x] 3.1 Go 单测：`TestCompileContinuationChainsAcrossPages`（3 页、两跳、offset 6000/11000、
  每跳 Props 与源段一致）、`TestCompileContinuationChainsStopsAtLastPage`（末页余量诊断）
- [x] 3.2 `go build/vet/test ./...` 全绿
- [x] 3.3 V19 正向：`-pages 3 -page-secs 6 -continuation` → **50/50**（含 `chain-0` 两跳，
  第 3 页配对显示「上一跳」）
- [x] 3.4 V19 负向：把第 3 页 offset 改错 1ms → **44/46**、退出码 1（V19.4 红）
- [x] 3.5 `npm run build` + `npm run build:share`（无漂移）通过
- [x] 3.6 `openspec validate animation-continuation-chain` 通过；归档后 specs 一致
