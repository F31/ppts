## 1. 基线先行（先量后做）

- [x] 1.1 量 H.264 编码损失上限。实测五页样本（CRF 18 / x264 medium / yuv420p → 解码）：
      0.99953 / 0.99977 / 0.99972 / 0.99949 / 0.99858。**天花板 ≥0.9986**，0.95 阈值余量充足，
      比较基准无需改成「与编码后的静态版比」。工具落在 `scripts/review/ssim.py`
- [x] 1.2 确认 M6-1 播放器**不校验** `schemaVersion`（`main.ts` 只在类型里有该字段，
      `mount()` 只判 `manifest.slides` 是数组）。结论已写进 design ADR-6；新播放器的拒绝判定放在 4.1
- [x] 1.3 量分层位图体积：5 页 19 层 = 377.6 KB（PNG）；全转 JPEG q82 = 267.2 KB（省 29%），
      但文字层自身 SSIM 掉到 0.991（最差窗口 0.64）。**结论：统一 PNG**，20 页预算 ≈1.7 MB。
      层图实测全不透明（RGB，alpha 255）。已替换 design ADR-5 的原假设
- [x] 1.4 定清单 `layers` 字段与类型：`shared/types.ts` 与 IR 同名同形
      （`elementId`/`kind`/`box`/`clips`），不做短名压缩

## 2. 共享插值抽取（解开 animation-render-service-image 的 2.9）

- [x] 2.1 `renderer/src/motion.ts` 提到 `shared/motion.ts`；`renderer/src/motion.ts` 删除，
      `Timeline.tsx` 与 `degrade-check.mjs` 改指向新位置；`renderer/src/types.ts` 改为
      re-export `shared/types.ts` 的动画类型。两侧 `tsconfig.include` 均加入 `../shared`
- [x] 2.2 `shared/motion-check.mjs`：8 条语义用例（opacity 相乘 / translate 相加 / scale 相乘 /
      clip 顺序无关 / 零长 clip 起点后恒 1（含起点前仍为 0）/ 未知属性忽略 / 贝塞尔端点与单调性 /
      帧数口径）。`node --test` 8/8 通过
- [x] 2.3 两侧构建产物确认引用同一份：`renderer` 的 `degrade-check.mjs` 与 `web` 的
      `animation-check.mjs` 都 import `shared/motion.ts`，`vite build:share` 产物从 6.87 KB
      增到 11.80 KB（多出来的是完整插值代码，而不是另一份手写实现）；重跑构建后
      `internal/share/assets/` 零漂移（证明组 2 是纯搬家）

## 3. Go 侧分层出图与打包

- [x] 3.1 `share.BundleInput` 增 `Pages []PageAnimation`（底板字节 + 转场 + 层素材）。
      打包器**不 import render**：分层位图由调用方算好，`share` 仍只依赖 `media`/`animation`，
      「打包」与「渲染」两个包的依赖方向保持不变
- [x] 3.2 挖空与出层图在**原始 PNG** 上做：`bundleAnimations` 用 `render.BuildPlate`/
      `BuildLayerAssets` 生成，再 PNG 编码；`BundleInput.PagePNGBytes` 让调用方把已读入的字节
      传进来，同一份对象不再读两次（副本路径仍过 `checkResourceKey`，key 合法性不因字节来源改变）
- [x] 3.3 层图命名 `slides/%03d-layer-%02d.png`，层序与清单下标一致（排查时能从文件名定位到清单第几项）。
      层图直接进 zip、不进对象存储，因此没有回收问题；层数由 IR 的轨道数决定，不另设上限
- [x] 3.4 `schemaVersion` 升 2；`slides[i]` 增 `plate`/`transition`/`layers`；`Validate` 追加四条约束：
      有层必须有底板（否则同一元素被画两次）、层 box 必须为正、层图必须在包里、转场类型白名单
- [x] 3.5 开关关闭时 `layers` 全空、`plate=false`、`image` 仍是 `.jpg`（走原 JPEG 转码路径）。
      用例 `TestBuildWithoutAnimationStaysStatic` 钉住这三点；另有 `TestMissingPageNeverReusesPlate`
      覆盖「缺图页不得沿用上一页底板」（沿用会露出一块纯色疤）

## 4. 播放器动画层

- [x] 4.1 编译：`web/src/sharePlayer/animation.ts` 的 `compileKeyframes` 把 `clips` 采样成
      WAAPI keyframes（步长 1000/fps），只覆盖 clip 活动区间 + 整页起终点，offset 严格递增
      （重复 offset 会让画面跳一下）。求值直接调 `shared/motion.ts`，播放器内没有第二份插值
- [x] 4.2 回放：WAAPI 一律 `pause()` 后设 `currentTime`（`seekLayer`），从不 `play()`——
      播放推进由 <audio> 负责，动画只是被"拖动"，因此不存在两套时钟抢着走的问题；
      拖动进度条走 `seek()` → `render()` → `seekLayer`，中途不改变 animation 的播放状态
- [x] 4.3 切页 `releaseLayers()`：`cancel()` + `remove()`。只 `pause()` 会把层留在合成层上，
      20 页后是几百个对象，且旧层图片仍会盖住新页
- [x] 4.4 时钟跟 `audio.currentTime`：`render(us)` 里 `syncLayers(us)`，rAF 只读时钟不累加
- [x] 4.5 无 WAAPI 降级已实测：`Element.prototype.animate` 置 `undefined` 后，4 个层的计算样式
      全是 `transform: none; opacity: 1`（终态可见，不是停在 opacity:0 的空白页）；
      该页在 clip-mid 的截图与 MP4 末帧 SSIM = **0.99953**
- [x] 4.6 `prefers-reduced-motion: reduce` 已实测（`--force-prefers-reduced-motion`）：
      同一时刻默认态首层为 `translateY 0.4757px`，降级态为恒等；不透明度两态都是
      **0.960779 / 0.814493**，完全相同 —— 位移去掉、时长不变
- [x] 4.7 页间转场（spec 要求，原任务清单漏列）：舞台改双面板，`prev` 面板取前一页末帧、
      `cur` 面板取当前页，三种类型与 `renderer/src/Timeline.tsx` 的 `PageEntry` 逐条对齐
      （fade 双向透明度 / slide 只推入 / zoom 淡入 + 1.06 倍收回，未知类型按 fade）。
      **这一项是页首帧从 0.85–0.91 拉到 0.998 的唯一原因**：MP4 不是"只换图"，
      而是"前一页末帧 + 当前页交叉"，DOM 只换图必然对不上

## 5. 门禁工具与验收

- [x] 5.1 `scripts/review/dom-ssim.py`：MP4 用 `ffmpeg -ss` 取帧，无头 Chrome 打开成品包
      （走真实控件：设 seekBar 再派发 input，不碰播放器内部函数）截图，算 SSIM。
      SSIM 复用 `scripts/review/ssim.py` 的 11×11 高斯窗实现，只依赖 numpy+Pillow。
      **必须带 `--run-all-compositor-stages-before-draw`**：否则截图会抢在光栅化之前，
      层多的页拍成黑屏（实测 page3 首帧亮度 8/255、SSIM 0.037，而同时刻 DOM 快照完全正常）——
      这是截图管线的竞态，不是播放器的问题，但会让门禁长期假红
- [x] 5.2 取样点：每页首帧 + **第一条带 props 的 clip 的中点** + 末帧前一帧（duration-34ms）。
      中点取第一条而不是最长的：第一条通常是标题入场，是每页最早运动的元素，也最容易做错
- [x] 5.3 逐页**最差帧** ≥ 0.95。实测（5 页 / 15 帧）：`0.99357 0.99786 0.99578 0.99818 0.99726`，
      最差帧 page 0 clip-mid@450ms = 0.99357 → PASS。首轮曾有一页末帧 0.166，
      排查结论是**截图布局**（`max-width:1280` + 24px 内边距把画面缩到 16.9%），不是播放器
- [x] 5.4 静态回归：`TestBuildWithoutAnimationStaysStatic` 追加字节级判据——包里的
      `slides/001.jpg` 必须**等于** `EncodePage` 的输出（转码质量/尺寸没被分层分支顺带换掉），
      且归档里不得出现任何 `-layer-` 条目。口径说明：M6-1 的端到端包未留存二进制基线，
      所以钉的是「同一转码路径」而非跨版本 zip 比对
- [x] 5.5 体积基线（实测 5 页 / 30 秒成品包，无音轨）：总 669 KB，其中底板 276 KB
      （53.9 KB/页）、19 张层图 386 KB（19.9 KB/层）、播放器 15.6 KB、清单 6.7 KB。
      每页 ≈129 KB → **20 页 ≈2.5 MB + 播放器 15 KB**；音轨另计（由 TTS 码率决定，
      与本次改动无关）。层数按每页 3.8 层的实测均值外推

## 6. 文档与收口

- [x] 6.1 本变更补 `README.md`：清单 v1→v2 对照、包结构新增条目、分层位图的来源与
      「为什么统一 PNG」、播放器回放方式、六种降级行为、体积代价（669 KB / 5 页）、
      门禁怎么跑、以及与计划的偏差表
- [x] 6.2 路线图「当前进度」加 `html-bundle-animation` 行、计划表第 6 行改为已归档并写明
      `targets`/`domFallback` 移出范围；`animation-render-service-image` 的 2.9 勾掉，
      注明「由 M6-2 的 `shared/motion.ts` 落实」，同时保留当时「等第二个调用方出现」的
      推迟理由作为决策记录
- [x] 6.3 `openspec validate --all` 通过（7 passed / 0 failed）；`openspec archive
      html-bundle-animation` 归档，delta 吸收进 `openspec/specs/html-share-bundle`
