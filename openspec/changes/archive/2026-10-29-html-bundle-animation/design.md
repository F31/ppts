# 设计：HTML 成品包的 DOM 关键帧

## 1. 分层与职责

```
internal/app/export.go          packHTMLBundle：拿 snapshot 的动画开关与规格身份
  └── animationPageInputs()     复用 M3 的 AnimationRequestBuilder（同一个装配器）
        └── render.Compiler     编译出 CompiledTimeline（与 MP4 通道同一份 IR）
              ├── MP4：/v1/render（Chrome + Remotion）
              └── 本变更：包内 layers 段（Go 出图，播放器编译关键帧）
internal/share.BundleBuilder    打包：清单 v2 + 底板 + 图层位图 + 播放器
  └── web/src/sharePlayer       运行期：WAAPI 回放，时钟跟 audio.currentTime
```

分工的判据只有一条：**像素由谁产生**。位图在 Go 里产生（`plate.go`，无 Chrome 依赖），关键帧在构建期产生（Node，读 IR），运行期只做时间轴同步。播放器里不出现任何缓动数学——这不是洁癖，是因为运行期求值会掉帧，而掉帧在 30fps 的成片里就是「HTML 比 MP4 卡」的直观证据。

## 2. 数据契约：清单 v2

`ManifestSchemaVersion` 从 1 升到 2，新增一段（其余字段不动）：

```jsonc
{
  "schemaVersion": 2,
  "slides": [
    {
      "slideId": "s1", "startUs": 0, "endUs": 6000000,
      "image": "pages/001.jpg",          // 挖空底板（无动画时为原底图）
      "plate": true,                      // image 是否已是挖空底板
      "transition": { "type": "fade", "durationMs": 400 },
      "layers": [
        {
          "elementId": "title-1",
          "image": "pages/001-layer-00.png",
          "box": { "x": 120, "y": 96, "w": 800, "h": 120 },   // 画布像素
          "clips": [                                           // IR 原样，不预编译
            { "clipId": "title-1-in", "effect": "slideIn",
              "startMs": 100, "durationMs": 700,
              "easing": { "x1": 0.16, "y1": 1, "x2": 0.3, "y2": 1 },
              "props": [ { "name": "opacity", "from": 0, "to": 1 },
                         { "name": "translateY", "from": 48, "to": 0 } ] }
          ]
        }
      ]
    }
  ]
}
```

三条兼容规则：

1. **旧播放器读新包**：不会拒绝（M6-1 的 `mount()` 不校验版本），会把挖空底板当完整页显示。但包内自带播放器，正常使用不会跨版本混读——这条只在「清单被手工换进旧包」时才成立，见 ADR-6。
2. **新播放器读旧包**：无 `layers` 即无动画，走 M6-1 的切页逻辑，行为零变化；读到 `schemaVersion > 2` 必须明确拒绝而不是猜着播。
3. **开关关闭**：包内 `layers` 全为空、`transitions` 全为 `none`，与「这个功能不存在」等价——这条与 M3 的 `withAnimation` 判定完全同源，不新增第二个开关。

`layers` 里放的是 **IR 的 `clips` 原样**，不是预编译的 keyframe 数组。这条改过一次，原因值得记下来：最初想在导出时（Go 进程）把 IR 编译成 WAAPI keyframes，但 Go 侧只有 `animation.CubicBezier` 这个数据结构与 `EasingCurve` 命名曲线，**没有任何求值器**——插值只在 `renderer/src/motion.ts` 里。要在 Go 里编译就得照着写第二份 cubic-bezier 求解，这正是 2.9 要消除的漂移源。

所以编译位置改到**播放器首次进入某页时**，一次性完成（一页几条 clip，代价是毫秒级），回放仍走 WAAPI。这样清单更小、更忠实于 IR（单一事实源），而求值代码是真共享，不是「两端各写一份、靠门禁对齐」。

## 3. 关键决策

### ADR-1 DOM 端复用 IR 的分层位图，不用整页变换，也不用文字重绘

三个方案都核过：

| 方案 | 可行性 | 否决理由 |
|---|---|---|
| 整页底图 + 整页变换 | 容易 | 只能做页级转场。元素级入场（标题先入、bullet 后入）无法表达；同帧 SSIM 必然远低于 0.95——因为 MP4 那一帧里 bullet 还没出现，HTML 里已经在了 |
| 用文字/形状重绘 DOM | 不成立 | IR 的 `Layer` 只有 `ElementID/Kind/Box/Clips`，**没有文本内容、字号、颜色**。重绘出来是空白矩形。要重绘就得扩 IR 带样式，那是另一条 L3 路线（`animation-vector-redraw`），且会立刻撞上字体度量一致性问题 |
| **分层位图 + 按 box 定位** | 已验证 | `plate.BuildPlate` / `BuildLayerAssets` 在 M2 已为 Remotion 侧实现并修过两个真缺陷（ADR-6 残影、ADR-9 嵌套裁切）。DOM 端只要用同样的输入做同样的事 |

选第三条。它的代价（体积）是可见的、可预算的；另两条的代价（做不到 / 做了也是错的）不可接受。

### ADR-2 编译在「首次进入该页」时一次完成，回放走 WAAPI

IR → WAAPI 的三步：

1. **属性展开**：`props[]` 里每条 `{name, from, to}` 变成一组 keyframe。`scale` 同时写 `scaleX/scaleY`（WAAPI 的 transform 是整体字符串，需要在编译期拼好）。
2. **多 clip 合并**：同一元素上作用于同一属性的多条 clip，若时间区间**不重叠**，各自成为独立 animation（顺序无关，语义天然保住）；若**重叠**，在该区间内按 1/30s 采样合并成一条 keyframe 序列。采样直接调 `motion.ts` 的 `evaluateLayer` —— **这就是 2.9 要的共享插值**：DOM 端不另写求解器，而是把同一份求值代码用在编译这一步。
3. **缓动序列化**：单条 clip 用精确 easing（`cubic-bezier(x1,y1,x2,y2)`，IR 的 `Easing` 四点直出）；合并后的序列用线性 easing + 逐段采样值。

编译时机选在「首次进入该页」而不是导出时，理由见 §2 末段：Go 侧没有求值器，在那里编译等于造第二份。选在「首次进入」而不是「播放开始」也有考虑——切页本身已经有 100–200ms 的图片解码开销，几毫秒的编译藏在里面看不见；而在播放开始时统一编译 20 页，会拖慢首次点击到出声的时间。

为什么回放不用 JS 逐帧求值：WAAPI 的 `transform`/`opacity` 走合成层，逐帧写 style 会触发布局与重绘，30fps 的 1080p 页面上会明显掉帧。方案 §20.5 已定，本变更把它落成「编译一次、回放零 JS」。

### ADR-3 同一属性多条 animation 的叠加语义

WAAPI 默认 `composite: "replace"`——同一元素上两条 opacity animation，后者会**覆盖**前者。而 IR 的语义是 **opacity 相乘、translate 相加、scale 相乘**（`motion.ts` 的 `evaluateLayer`）。

若不做处理，「两个淡入叠一起」在 MP4 里是 0.25×0.5=0.125，在 HTML 里是 0.5。这类差异肉眼看不出，但 SSIM 会掉，且没人能解释为什么掉。

处理办法按代价从低到高：

1. **不重叠 → 天然正确**（绝大多数情况，一个元素一条入场）。这是优化的主路径，不是特例。
2. **重叠 → 构建期采样合并**（ADR-2 第 2 步）。语义直接由 `evaluateLayer` 保证。
3. 不使用嵌套 wrapper 承担叠加：wrapper 会让 DOM 层级与 `Box` 计算脱节，且 transform 的复合顺序（`translate` 后 `scale` 还是反之）在两端容易写反——那是一种「看起来能跑、像素对不上」的缺陷，比采样误差难查得多。

### ADR-4 时钟跟音频，不跟 rAF

播放器已有 `audio.currentTime` 作为全局时间（`audioMix` 单轨）。动画层只订阅它：

- 播放：`audio.play()` 之后所有 `Animation.play()`，每帧用 `currentTime` 校正一次 `anim.currentTime`
- seek：先 `pause()` 全部 animation，批量设 `currentTime`，再按播放状态决定是否 `play()`
- 切页：把上一页的 animation 全部 `cancel()`（不是 `pause()`——留着会占用合成层，20 页之后就是几百个对象）

不自己计步的理由很直接：`requestAnimationFrame` 的累计误差在 5 分钟里是秒级，而音频漂移是观众第一耳朵能听出来的问题。方案 §20.5 已定，这里只是落实。

### ADR-5 层图统一用 PNG，不切 JPEG 分支（实测后修正）

原本按「文字层 PNG、其余 JPEG q82」设计，实测推翻了它。5 页样片 19 层、377.6 KB，全部转 JPEG q82 后：

| | 体积 | 文字层自身 SSIM |
|---|---|---|
| 全部 PNG | 377.6 KB | 1.0（基准） |
| 全部 JPEG q82 | 267.2 KB（省 29%） | 0.991，最差窗口 0.64 |

省 29% 换掉文字边缘质量的 1 个点，而门禁阈值是 0.95——层图自己先亏 1 分，再加上 H.264 编码损失，余量就被吃掉了。而全 PNG 的预算根本不需要省：19 层 / 5 页 = 3.8 层/页，20 页 76 层 × 22 KB ≈ **1.7 MB**，加底板 1.6 MB + 音频 2.4 MB ≈ **5.7 MB**，仍在 8 MB 内。

所以第一版统一 PNG。真正的体积优化（WebP、子集化）留给 M6-4，那时有统一的质量门禁兜底，不会拿 SSIM 换体积。

层图实测**全不透明**（`RGB`，alpha 全 255），所以「能不能用 JPEG」这个前提本身是成立的——否决它的不是格式限制，是质量账划不来。

### ADR-6 `schemaVersion` 升到 2 而不是加可选字段

加可选字段（`layers` 直接塞进 v1 的 slide 条目）在 JSON 层面是合法的，旧播放器会忽略它。但它掩盖了一个事实：**v2 包的 `image` 语义变了**——从「完整页面」变成「挖空底板」。

先核过一件事再定这条：M6-1 的播放器**并不校验** `schemaVersion`（`main.ts` 只在类型里有这个字段，`mount()` 只判 `manifest.slides` 是不是数组）。所以「旧播放器拒绝 v2 包」这句话不成立——旧播放器会把挖空底板当完整页显示，元素位置是一块纯色疤。

真正的风险场景也因此收窄了：**包内自带播放器**，旧包配旧播放器、新包配新播放器，正常使用不会跨版本混读。剩下的风险是「有人把新包的清单喂给旧播放器」——这个场景不存在现实入口，但**版本位仍然要留**，用于两件事：

1. 新播放器读到 `schemaVersion > 2`（未来版本）时必须明确拒绝并提示，而不是猜着播。
2. 排查用户反馈时，一眼能看出这个包是「有动画的还是纯静态的」，不用去翻 `layers` 是否为空。

所以 v2 同时保留 `schemaVersion: 2` 与「无 `layers` 即无动画」两条判定，前者管跨版本，后者管同版本内的能力差异。

### ADR-7 SSIM 门禁要取「动画进行中」的帧

M6-2 的验收写着「两端同帧 SSIM ≥ 0.95」。最容易糊弄的过法是取末帧：动画早已结束，两端都是静态页，SSIM 会到 0.99 以上，而这条门禁什么都没验。

门禁的正确形状：

1. 从 IR 里找出每页**第一条 clip 的中点时点**（`startMs + durationMs/2`），对每个这样的时点取帧——这个时刻元素正处在运动中间，位置/不透明度都在半途，任何一端算错都会暴露。
2. 同时取每页**首帧**与**末帧**兜底（前者验初始状态，后者验落位）。
3. MP4 侧：`ffmpeg -ss <t> -i segment.mp4 -frames:v 1` 解出一帧。
4. DOM 侧：无头 Chrome 打开成品包，pause 到同一 `currentTime`，截 1920×1080。
5. SSIM 用 `scripts/review/` 下的新工具算（复用 `compare.py` 的灰度与分窗逻辑，不引 scikit-image）。

阈值：逐帧 ≥0.95，且**逐页取最差帧**判定——不取平均，平均会把一页的失败摊薄到 20 页里。

**编码损失已实测**（tasks 1.1）：五页样片各走一遍「CRF 18 / x264 medium / yuv420p 编码 → 解码」，与原图的 SSIM 分别是 0.99953 / 0.99977 / 0.99972 / 0.99949 / 0.99858。**天花板在 0.9986 以上**，0.95 的阈值有充足余量，说明「MP4 解码帧 vs 浏览器截图」这个比较基准是对的，不需要改成「与编码后的静态版比」。

这条实测很有必要：如果天花板本身就在 0.94，那门禁永远过不了，而失败会被误读成「DOM 动画实现有问题」——排查方向从一开始就是错的。

已落地的工具：`scripts/review/ssim.py`（11×11 高斯窗、K1=0.01/K2=0.03、无需 scipy），支持 `--worst` 定位最差窗口、`--grid N` 分出最差块，阈值可用 `--min` 覆盖。

## 4. 验收

| 项 | 门禁 |
|---|---|
| 解压双击可播 | 无网络、`file://` 下能播完整片（M6-1 的用例保留） |
| 两端同帧 SSIM | 逐页取动画中点帧 + 首末帧，**最差帧 ≥ 0.95** |
| 开关关闭 | 包内 `layers` 为空、`schemaVersion` 仍为 2、画面与 M6-1 逐像素相同 |
| 静态回归 | 无动画的包与 M6-1 产物字节级比较（除 `createdAt`/`snapshotHash`） |
| 体积 | 5 分钟 / 20 页的包体积记录基线，写进 README |
| 无 WSAPI 降级 | 删掉 `Element.animate` 后播放器仍能播（静态切页），不抛错 |

## 5. 风险

| 风险 | 影响 | 处置 |
|---|---|---|
| 分层位图体积失控 | 包从 1.6MB 涨到 10MB+，撞穿分享渠道 | ADR-5 分格式 + tasks 1.4 先量基线；不达标则退「只对文字层分层」 |
| 两端像素差异集中在文字边缘 | SSIM 卡在 0.93 左右 | 编码损失已实测 ≥0.9986（ADR-7），余量足够；剩下的变量是层图按 box 放置时的缩放与抗锯齿——层图尺寸若与 box 不等比，浏览器会插值，必须按 1:1 放置 |
| 旧播放器读到 v2 包显示挖空底板 | 用户看到「画面有疤」 | ADR-6：版本位 + 明确拒绝 |
| `plate.go` 的输出依赖 JPEG 压缩前的原图 | 打包链路里若先转码再挖空，取色会偏 | 挖空必须在**原始 PNG** 上做，转码在其后（tasks 1.5 的用例） |
