# 任务：页内镜头切片（Page.Shots）

> 三条设计取舍已定（120ms 叠化 / 每页 ≤4 镜 / 渲染侧产 2x），契约见 `design.md`。前四组做完就有可见效果，第五组只影响清晰度。

## 0. 前置（调研已完成，留档）

- [x] 0.1 全仓库无 Scene/Shot/Camera 结构体；`render.Page` 是唯一的时间与内容单位（`ir.go:63-92`）
- [x] 0.2 一页内多图层 × 多 clip 已支持，clips 为页内相对时间——这就是「一页多镜」不必改数据模型的证据
- [x] 0.3 硬缺口只有一处：`Page` 一张底图 + `service.go:630` 一页一段
- [x] 0.4 页边界必然 `rebuildLayers`（`main.ts:388-400`），所以「跨页连续」不是改个判断就行，属 G8
- [x] 0.5 渲染侧只有 `RenderOptions{DPI}`，无区域参数；2x 特写 = 高 DPI 重渲 + 裁切

## 1. IR + 校验 + golden

- [x] 1.1 `render.Page` 增 `Shots []Shot`；`Shot{Index, Image, Crop, StartMs, DurationMs, CrossFadeMs}`
- [x] 1.2 校验：首尾相接 / 总长 == 页时长（±1 帧）/ `Crop` 在原页范围内 / 每页 ≤4 镜 / `CrossFadeMs` < 镜头时长 / 首镜 `image` == 该页的呈现底图（有底板时为底板）/ 首镜不得声明 `crop` / `index` == 位置（宽高比那条挪到打包期，IR 里 Image 只是路径、量不了）
- [x] 1.3 无 `shots` 时输出与今天字节级一致（golden 守，不靠人看）

## 2. 播放器 + 渲染端

- [x] 2.1 `main.ts` 页内按 `us` 选当前镜头，底图 `src` 在镜头边界切换（`syncShot` 每帧求值，按 `dataset.shot` 去重，不做无条件赋值）
- [x] 2.2 叠化：双 `<img>` 叠放，占后一镜开头 `crossFadeMs`，`easeOutCubic`（曲线提到 `shared/motion.ts` 导出，页间转场/焦点/叠化共用一份）
- [x] 2.3 层不重建（clips 是页内相对）；`Crop` 非空的镜头内层不显示（`visibility`，不摘节点）。两条都写了注释
- [x] 2.4 降级路径（无 WAAPI / reduced-motion / 低电量）下镜头仍切——结构上就不依赖：`syncShot` 只碰 `src` 与 `visibility`，不进 WAAPI、不看 `lowPower`
- [x] 2.5 `Timeline.tsx` 同一页内按 `tMs` 选图 + 叠化（与播放器共用 `shared/shots.ts` 的求值）

> 实测（本机，2 页 × 4s 夹具，120ms 叠化）：播放器 1500ms→整页、2064ms→两张图同时在场、2400ms→纯特写，与源图逐像素 meanDiff ≤0.002；渲染端 `still.mjs` 1000ms→整页、2067ms→混合、2500ms→特写，同量级。两处各自踩到同一个坑（两张图会竖向排列、第二张跑出画框），已在两边分别修正并留注释。

## 3. 清单 + 打包

- [x] 3.1 `share.SlideEntry` 增 `Shots`（`omitempty`）+ `SlideShot` 类型
- [x] 3.1b `bundle.go` 把 `PageAnimation.Shots` 写成包内文件并填清单。首镜复用页级图（同字节同文件名），第 2 镜起写 `slides/NNN-shot-0K.png`；打包期补上 IR 算不了的那条：`Crop` 宽高比与图相差 >1% 即失败
- [x] 3.2 校验三条：空洞/重叠/总长；`shots` 非空时 `slide.Image` == 首镜 `image`；`Crop` 在页内且宽高比在打包期核（IR 里 Image 只是路径）
- [x] 3.3 每个 `shot.Image` 必须在包内（`files` 集合）——3.1b 落地后这条有了真实输入
- [x] 3.4 **不升 `schemaVersion`**：查过播放器根本不读它，升版本只让 Go 的 `Validate` 拒绝 v2 包。改为在规格里写「旧播放器降级可播放、但不换镜」

## 4. 门禁 + 夹具

- [x] 4.1 V13b 取样改「末镜内、避开叠化、尽可能贴页尾」；参照物按 `crop` 选（为空取源页图，非空取镜头图）。**与原计划不同**：原稿写「取末镜中点、参照镜头图」，两处都不对——中点可能落进叠化，而末镜若是页级图（有层时是底板），层还看得见，拿底板当参照必然判错。实测：无镜头 0.99994（与改动前一致）、末镜特写 0.99994、末镜图 640×360 时 0.99722（参照图缩放，走单独一组阈值）
- [x] 4.2 V13 取样加切点：取 `startMs + crossFadeMs + 1 帧`（不取切点本身——混合窗口只有 120ms，两端时钟差 20ms 就会被记成「两端不一致」）。实测 2 页 × 2 镜夹具：逐页最差 0.99679 / 0.99646
- [x] 4.3 新增 `scripts/review/shot-gate.py`（V18）：切点前 1 帧 / 叠化中点 / 切点后 1 帧三处取样，加「层节点身份不变」与「层可见性 == 是否特写」两条跨点判据。**负例实测**：把包内 `app.js` 换成引入镜头之前的版本（清单仍有 shots，播放器只认 `slide.image`）→ 切点后两条判据红、退出码 1
- [x] 4.4 `make-fixture.mjs --shots N`：双镜页进夹具，`plan.json` 带 `shots`。V13/V13b 对无 shots 的包读数不变（0.99994 / 0.99646–0.99772 同量级）
- [x] 4.5 探针加「当前面板」结构化读数（两张图的 src/可见性/不透明度、层节点身份、层可见性），并把定位换成播放器的微秒接口——滑杆千分比在 40 分钟的作业上一步是 2.4 秒，而这里要取 33ms

## 5. 2x 素材（后置，只影响清晰度）

- [x] 5.1 对声明 `Crop` 的页，导出时 `RenderOptions{DPI:300}` 重渲一次整页
- [x] 5.2 按 `Crop` 裁切并作为 `Shot.Image` 落盘；同页多个特写共用一次渲染
- [x] 5.3 验收用可量的判据：特写图边缘梯度能量 ≥ 300 DPI 源裁切的 95%（不看「看起来清楚了吗」）

## 6. 文档

- [x] 6.1 `render-plan` 规格补 Shot 语义、校验与「crop 镜头内层不显示」（增量见 `specs/render-plan/`）
- [x] 6.2 `html-share-bundle` 规格补清单字段与「首镜复用页级图」；**改掉原计划里的 schemaVersion 3**，并单列一条 Requirement 说明为什么不升（增量见 `specs/html-share-bundle/`）
- [x] 6.3 路线图补一行，写明它与 G7/G8 的边界
