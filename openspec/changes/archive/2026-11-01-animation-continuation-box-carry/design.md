# 设计：静态盒补偿（carry）

## 1. 问题定义

设元素在页 N 的盒为 `B = (Bx, By, Bw, Bh)`，在页 N+1 的盒为 `C = (Cx, Cy, Cw, Ch)`。
延续段挂在页 N+1 的层上，因此页 N+1 的 `localMs = 0` 处，该元素的视觉矩形是 `C`（叠加动效），
而页 N 的页尾处是 `B`。二者相差 `(Bx-Cx, By-Cy, Bw-Cw, Bh-Ch)`——页缝跳变。

动效的连续性（负起点平移）对这个差**完全无感**：它保证的是 clip 进度的连续性，而盒是层的静态
属性，不参与 `evaluateLayer`。

## 2. 补偿的合成语义

两端渲染层的 DOM/CSS 结构一致（播放器 `buildLayerNodes`、渲染器 `Timeline.tsx`）：

```
left = box.x, top = box.y, width = box.w, height = box.h
transform: translate(tx, ty) scale(sx, sy)
transform-origin: O   (center | top | bottom | left | right)
```

按 CSS 语义（`translate` 在父坐标系、`scale` 绕 origin），层内局部点 `p` 映射到画布：

```
p' = box + O + s·(p - O) + t
```

于是视觉矩形：

```
左上 = box + O·(1 - s) + t
尺寸 = (sx·w, sy·h)
```

令 `p = 0` 时视觉矩形等于 `B`，`p = 1` 时等于 `C`（即 `s = 1, t = 0`）：

```
sx = Bw/Cw,  sy = Bh/Ch
tx = (Bx - Cx) - Ox·(1 - sx)
ty = (By - Cy) - Oy·(1 - sy)
```

其中 `Ox/Oy` 是锚点在**本页层**内的局部偏移（`center → (w/2, h/2)`，`top → (w/2, 0)`，……）。

**为什么补偿必须含 `O·(1-s)`**：`scale` 绕 origin 进行，尺寸变化会把原点之外的部分一起带走。
`center` 锚点、`sx = 0.8` 时，左上角会自移动 `0.1·w`。只用位置差补偿，页缝仍会偏这一段。
`internal/render/carry_test.go` 用 5 种锚点把这个项钉死。

这项性质也让「独立插值 `tx` 与 `sx`」是安全的：两者都是 `p` 的线性函数，所以视觉左上角在整个
收敛窗口内是 `B → C` 的线性插值，不存在中途的扭曲路径。

## 3. 收敛时长

`carryMs = min(500, clip 剩余时长, 本页时长)`，`< 1ms` 时不补偿。

- 500ms 是**修正**的时长，不是动效的时长。补偿的意义是「把这个错位尽快修掉」，铺满整条 clip
  会让元素整段戏都在缓慢漂移，看起来像没对准。
- 上限还受「clip 剩余时长」约束：clip 在本页只剩 120ms 时，补偿必须在 120ms 内收完，否则
  元素会在 clip 结束后还继续移动——那已经不属于这条 clip 的语义了。
- 500ms 在 30fps / 60fps 下都是整数帧。

## 4. 与既有字段的关系

- `isContinuation`：补偿段 MUST NOT 设置。它不是原 clip 的另一截；门禁的「源段配对」会按
  effect/duration/Props 找上一段，补偿段参与配对只会产生假阳性。
- `carry`：新字段（`Motion.Carry` / `share.Clip.Carry`，均 `omitempty`）。门禁据此把补偿段
  排除出配对，并单独校验「终点必须是恒等变换」。
- `ContinuationSource`：仍为编译器内部标记，不进清单。`bundleClips` 加一条 carry 透传，
  由 `internal/app/bundle_clips_test.go` 固定（share-baseline 绕过打包，端到端门禁覆盖不到）。

## 5. 不产生补偿的情形

| 情形 | 行为 | 理由 |
|---|---|---|
| 两页盒逐字段相同 | 不产生 carry | 作者按既有写法编排时，清单必须与引入前逐字节一致 |
| 承接页无同 elementId 层 | 不产生 | 延续段本身已记 `continuation_target_missing` |
| `carryMs < 1` | 不产生 | 没有收敛窗口，补偿只会变成一次瞬跳 |
| 补偿分量全为恒等 | 不产生 | `CarryProps` 返回 nil（防御性；盒不同时不可能全恒等） |

## 6. 验收

- `internal/render/compile_test.go`：盒差数值（含锚点项手算）、同盒不产补偿、链式每跳以
  上一跳的盒为参照、5 种锚点、`CarryDuration` 三条边界。
- `scripts/review/seam-gate.mjs`（V19）：新增 V19.8（视觉矩形连续，含补偿）、V19.9（补偿终点
  为恒等变换）、V19.10（包内必须存在盒差样本，防止夹具退回「两页同盒」后判据恒真）。
  V19.5 改为**排除** carry 后比较动效合成态——两者混在一起会互相抵消。
- 负向验证：删掉补偿段 → V19.8 报出真实盒差（Δx=160/Δw=80）；补偿量偏 10px → 8 条红；
  终点不收敛 → V19.8 与 V19.9 同红。
