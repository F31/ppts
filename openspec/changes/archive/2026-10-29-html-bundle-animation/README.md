# html-bundle-animation

M6-2：让静态成品包（`html_bundle`）带上与 MP4 同一套元素动画与页间转场

## 一句话取舍

**不重新设计一套 DOM 动画，而是把 IR 已经算好的东西再消费一遍。**
底板、层图、转场、clips 在 M2 就已经为 Remotion 侧产出过一次；M6-2 的活儿是
「把这些数据多带一个消费者」，不是「在浏览器里再做一遍动画系统」。

## 清单从 v1 到 v2 变了什么

`manifest.schemaVersion` 升到 `2`。变化只落在 `slides[i]` 上：

| 字段 | v1 | v2 |
|---|---|---|
| `image` | 完整页面（JPEG） | 有动画时是**挖空底板**（PNG）；无动画时语义不变 |
| `plate` | — | `true` 表示 `image` 已被挖空，元素本体由 `layers` 盖回去 |
| `transition` | — | `{type: none\|fade\|slide\|zoom, durationMs}`，挂在**后一页**，只占该页开头 |
| `layers` | — | 每层 `{elementId, kind, box, image, clips}`，按绘制顺序（父在前） |

`clips` 是 **IR 原样**，不是预编译的 WAAPI 关键帧。原因写在 `design.md` ADR-2：
在 Go 里编译就得照抄第二份 cubic-bezier 求解器，而求值代码只有 `shared/motion.ts`
一份——两份求解器的漂移只会在叠加 clip 的页上暴露，那时已经上线了。

## 包结构（相比 M6-1 新增）

```
slides/001.png            底板（被挖空的页面，元素位置已填平）
slides/001-layer-00.png   第 1 页第 0 层的位图（尺寸 = 该层的 box）
slides/001-layer-01.png   ...
```

- **层图统一 PNG**。实测把 19 层全转 JPEG q82 只省 29%（377.6 → 267.2 KB），
  但文字层自身 SSIM 掉到 0.991、最差窗口 0.64——省下的 110 KB 换不来这个代价。
- 层图直接进 zip，不进对象存储，因此没有孤儿回收问题。
- 挖空与裁层都在**原始 PNG** 上做（先转码 JPEG 再取色会偏），由
  `render.BuildPlate` / `render.BuildLayerAssets` 完成，打包器只负责写字节。

## 播放器怎么回放（`web/src/sharePlayer/`）

- **编译一次**：首次进入某页时把 `clips` 采样成 WAAPI 关键帧，采样步长 = 成片帧率。
  帧间线性插值是对的：MP4 在帧之间也没有定义。
- **回放只做 seek**：`animation.pause()` 之后只设 `currentTime`，从不 `play()`。
  推进时间的只有 `<audio>.currentTime`（rAF 只把时钟读出来），不存在两套时钟抢着走。
- **转场双面板**：`prev` 面板放前一页末帧、`cur` 面板放当前页，按 `easeOutCubic` 交叉。
  这跟 Remotion 的 `PageEntry` 同结构。页首帧曾只有 0.85–0.91，根因就是只换图不交叉。
- **层用百分比定位**（x/W、w/W），位移量是画布像素，乘 `--ppts-k`（1 画布像素等于
  多少实际像素）换算——写死 px 会让 4:3 的页在 16:9 的舞台上错位。

## 降级行为（都不报错，但表现必须可预期）

| 情形 | 行为 |
|---|---|
| 浏览器没有 WAAPI | 层停在**终态**（`opacity:1; transform:none`），页面完整可看，不空白 |
| `prefers-reduced-motion: reduce` | 去掉位移与缩放，**保留不透明度、保留时长**（缩短时长会让动画提前结束，与音画错位） |
| 单页读图失败 | 该页沿用上一页画面；若上一页是底板则退到占位图——绝不把挖空底板当完整页（会露出一块纯色疤） |
| 有 `layers` 但 `plate=false` | 打包期直接失败：同一元素会被画两次，入场期间能看到重影 |
| 播放器版本低于清单 | 拒绝播放并提示更新（`MAX_SCHEMA_VERSION = 2`）。宁可说「需要更新的播放器」，也不要静默播错 |
| 导出时动画计算失败 | 整包退化为静态（`pages = nil`）并记 warn，用户仍拿到一个能播的包 |

## 体积代价（实测，5 页 / 30 秒，无音轨）

| 分项 | 文件数 | 体积 | 备注 |
|---|---:|---:|---|
| 底板 `slides/NNN.png` | 5 | 276 KB | 53.9 KB/页 |
| 层图 `slides/NNN-layer-KK.png` | 19 | 386 KB | 19.9 KB/层 |
| 播放器 `app.js/app.css` | 2 | 15.6 KB | 含完整插值代码（11.8 KB JS） |
| 清单 `manifest.*` | 3 | 6.7 KB | |
| **合计** | 29 | **669 KB** | 每页 ≈129 KB |

外推：**20 页 ≈2.5 MB + 播放器 15 KB**（层数按每页 3.8 层的实测均值）。音轨另计，
由 TTS 码率决定，与本次改动无关。对比 M6-1 同期底图（JPEG q82）约 44 KB/页，
分层把每页成本抬到约 3 倍——这是「元素级动画」的定价，不是实现浪费。

## 门禁怎么跑

```bash
python3 scripts/review/dom-ssim.py \
    --plan tmp/5.5/job/plan.json \
    --pkg  tmp/m62/pkg \
    --segments tmp/5.5/out \
    --out  tmp/m62/gate
```

- 取样点：每页首帧 + **第一条带 props 的 clip 的中点** + 末帧前一帧。
  只取末帧等于什么都没验（那时动画早已结束，两端都是静态页）。
- 判定：逐页**最差帧** ≥ 0.95，不取全片平均（平均会把一页的失败摊薄到 20 页里）。
- 实测：`0.99357 / 0.99786 / 0.99578 / 0.99818 / 0.99726` → PASS。
- 截图必须带 `--run-all-compositor-stages-before-draw`：否则会拍到光栅化之前的黑屏
  （实测亮度 8/255、SSIM 0.037），而同时刻 DOM 快照完全正常——假红比假绿更浪费时间。
- H.264 编码损失天花板先量过：CRF 18 / x264 medium / yuv420p 实测 0.9986 以上，
  0.95 阈值余量充足，比较基准不需要改成「与编码后的静态版比」。

## 落地范围与偏差

| 计划 | 实际 | 原因 |
|---|---|---|
| catalog `targets` / `domFallback` | 未做 | 需要产品决策（哪些元素允许在 DOM 端降级），不在本变更能自动闭环的范围内 |
| `animation-track-editor`（M4） | 未做 | 涉及配额/灰度，同样需要产品决策；优先级让给可机器判定的 M6-2 |
| `schemaVersion` 升 2 并让播放器拒绝 | 升 2，但拒绝只挡「更高版本」 | M6-1 的播放器 `mount()` 根本不校验版本（只在类型里有该字段）；同包自带播放器不会跨版本混读，版本位的主要作用是排查 |
| 任务清单原列 4.1–4.6 | 追加 4.7 页间转场 | spec 要求转场，但任务清单漏列；门禁正是靠这一项把页首帧从 0.85–0.91 拉到 0.998 |
| 截图直接 `--screenshot` | 加 `--run-all-compositor-stages-before-draw` | 层多的页会拍到未完成光栅化的黑屏（见上节） |
| Windows 下 `docker -v E:\...` | 经 `wslpath` 转 `/mnt/e/...` | WSL 里的 docker 只认 Linux 路径，否则直接 exit 125 |
