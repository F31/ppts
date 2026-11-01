# 设计：链式多页延续

## 1. 核心不变式（一句话）

**每一跳的 `continuationOffsetMs` 等于「上一段在本页尾处的 clip 时间」。**

- 首跳：`offset₁ = 页长 - startMs`（源段在本页尾处的时间）；
- 第 k 跳：`offset_k = offset_{k-1} + 页长_{k-1}`；
- 每段都满足 `startMs = -offset`，因此 `evaluateLayer` 在任意一页的 localMs 处求得的进度
  `(localMs + offset) / duration` 都等于原 clip 在同一绝对时刻的进度。

链式没有引入新的求值语义，只是把单跳的公式沿页重复。这正是它能复用小改动的原因：
播放器、渲染器、`shared/motion.ts` 一行都不用改。

## 2. 编译器改动

`injectContinuations` 原来的结构是「算出次页段 → 注入 i+1 → 结束」。改为：

```
cur = seg（首跳）
for pageIdx := i+1; ; pageIdx++ {
    if pageIdx 越界            → 记链尾诊断，break
    if 该页无同 elementId 层    → 记目标缺失诊断，break（已注入的段保留）
    注入 cur 到该页
    if cur.offset + 页长 + 1帧 >= cur.duration → break   // 本页内演完
    cur.offset += 页长
    cur.start = -cur.offset
}
```

1 帧容差与 `compileClip` 的溢出判定同口径（`frameMS(fps)`），避免「差半帧就多切一段」。
段对象按值复制后再改：`cur := *seg`，防止首跳被后续跳覆盖。

## 3. 校验

`validateMotion` 原本只豁免 `ContinuationSource`（源段）。链式延续段同样会超页，必须一并豁免：

```
if !m.ContinuationSource && !m.IsContinuation && m.StartMs+m.DurationMs > pageMS+frameMS { error }
```

为什么这是安全的：两类段的超页余量都有明确承接方——源段由第 1 跳承接，延续段由下一跳或片长承接。
`startMs < 0` 仍只对 `IsContinuation` 放行，非延续 clip 的负起点依旧非法。

## 4. 门禁（V19）

配对判据推广为「expectedOffset」：

| 前一页的配对段 | expectedOffset |
|---|---|
| 源段（`isContinuation=false`） | `前一页页长 - 该段 startMs` |
| 上一跳延续段（`isContinuation=true`） | `上一跳 offset + 前一页页长` |

两者本质相同（都是「上一段在本页尾处的 clip 时间」），所以链式不需要第二套判据。
`V19.5` 的页缝求值连续对**每一跳**都单独执行——链上任意一处写错都会红，而不是只验首跳。

## 5. 夹具

`share-baseline -continuation` 在 `-pages >= 3` 时注入 `chain-0`：

- 页 0：源段，`start = 页长 - offset`，`duration = offset + 2×页长 - 1s`；
- 页 1：第 1 跳，`offset = 3s`；
- 页 2：第 2 跳，`offset = 3s + 页长`；此时 `offset + 页长 >= duration`，链在此收尾。

`duration` 刻意取「正好在第 3 页内演完」：末页余量是另一条语义（被片长裁掉），由 Go 单测
`TestCompileContinuationChainsStopsAtLastPage` 覆盖，不塞进浏览器包，避免夹具承担两种意图。

## 6. 失败形态与诊断

| 情形 | 行为 |
|---|---|
| 链中某页无同 `elementId` 层 | 停止后续注入，记 `continuation_target_missing`；**已注入的段保留**（元素在中断页之前仍连续） |
| 到达末页仍有余量 | 记 `continuation_target_missing`，余量被片长裁掉，不阻断 |
| `duration ≤ 0` / 起点超页 | 与单跳一致：记诊断并退化为本页截断 |

诊断的 `Page` 沿用既有口径（源页 `in.Index`），不指向中断页——与单跳的其余诊断一致，
避免调用方按 Page 分组时出现两套语义。
