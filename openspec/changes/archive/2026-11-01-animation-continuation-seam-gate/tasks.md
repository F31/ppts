# 任务清单

> 一个里程碑 = 一次 commit + push + 全量验证。本变更只有一个里程碑（M1）。

## 1. 夹具与门禁

- [x] 1.1 `share-baseline` 增 `-continuation`（默认关）：前置 `-layers>0` / `-pages>=2` / `页长 > offset`
  不满足时 `log.Fatal`，不静默跳过；夹具常量提为 `continuationOffsetMs` / `continuationDurationMs`，
  源段起点由 `页长 - offset` 推导，延续段 `startMs = -offset`
- [x] 1.2 延续夹具补齐 **clip 级 `elementId`**（此前只给了 layer 级，清单里缺链接键；V19.1 首次运行即抓到）
- [x] 1.3 新增 `scripts/review/seam-gate.mjs`（V19）：读成品包 `manifest.json`，用 `shared/motion.ts`
  断言 V19.0–V19.7（链接字段 / 负起点 / 源段配对 / 页缝求值连续 / 中途态 / 非空跑）
- [x] 1.4 CI 增两步：`share-baseline -pages 3 -page-secs 6 -layers 2 -continuation` 造包 + 跑 V19；
  放在浏览器门禁之后，复用同一作业已装的 Go/Node
- [x] 1.5 负向验证：无延续段的包 → 退出码 1（1/2 通过）；篡改 `startMs` 与 `props.from` 的包 →
  退出码 1（20/24 通过，V19.2/4/5/6 红）

## 2. 规格对齐

- [x] 2.1 `html-share-quality` MODIFIED 现有条目：判据从「页取样抽帧」修正为「成品包清单 + 共享求值器」，
  并写明延续元素 MUST NOT 进入 V13/V13b 的末帧比对、夹具 MUST 显式开关
- [x] 2.2 归档的 G8 `proposal.md` 追加「实施结果（归档补记）」：指明「次页发 `[0,余量]`」的旧描述已被
  负起点平移取代，`design.md` §2 为准

## 3. 验证

- [x] 3.1 `go build ./...` / `go vet ./...` / `go test ./...` 全绿
- [x] 3.2 `npm run build`（tsc -b + vite build + 六项检查）通过；`npm run build:share` 无漂移
- [x] 3.3 `node --experimental-strip-types --test shared/shots-check.mjs` 与 `web/animation-check.mjs` 通过
- [x] 3.4 `openspec validate animation-continuation-seam-gate` 通过；归档后主 specs 一致
