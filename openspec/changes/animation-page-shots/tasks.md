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
- [x] 1.2 校验：首尾相接 / 总长 == 页时长（±1 帧）/ `Crop` 在原页范围内 / 每页 ≤4 镜 / `CrossFadeMs` < 镜头时长 / 首镜 `image` == `baseImage` / `index` == 位置（宽高比那条挪到打包期，IR 里 Image 只是路径、量不了）
- [x] 1.3 无 `shots` 时输出与今天字节级一致（golden 守，不靠人看）

## 2. 播放器 + 渲染端

- [ ] 2.1 `main.ts` 页内按 `us` 选当前镜头，底图 `src` 在镜头边界切换
- [ ] 2.2 叠化：双 `<img>` 叠放，占后一镜开头 `crossFadeMs`，`easeOutCubic`——与页间 `fade` 同一套，不新写插值
- [ ] 2.3 层不重建（clips 是页内相对）；`Crop` 非空的镜头内层不显示。两条都要写成注释，否则下一个人会顺手重建或让层错位
- [ ] 2.4 降级路径（无 WAAPI / reduced-motion / 低电量）下，镜头仍要切——降级的是动画，不是画面
- [ ] 2.5 `Timeline.tsx` 同一页内按 `tMs` 选图 + 叠化

## 3. 清单 + 打包

- [ ] 3.1 `share.SlideEntry` 增 `Shots`（`omitempty`），`bundle.go` 按 IR 原样写入
- [ ] 3.2 校验三条：空洞/重叠/总长；`shots` 非空时 `slide.Image` == 首镜 `image`；`Crop` 宽高比与 `Image` 差 ≤1%（这条只能在打包期做——IR 里 Image 只是路径）
- [ ] 3.3 每个 `shot.Image` 必须在包内（复用 `checkBundlePath` 与 `files`）
- [ ] 3.4 `schemaVersion` 升到 3，并写清「旧播放器读到 shots 会退化成首镜」不是兼容

## 4. 门禁 + 夹具

- [ ] 4.1 V13b 取样改「最后一个镜头的中点」，参照物是该镜头的 `Image`
- [ ] 4.2 V13 取样加「每个镜头切点后 1 帧」
- [ ] 4.3 新增切图判据：`startMs-1帧` 是第 1 张、`startMs+crossFadeMs+1帧` 是第 2 张，且中间层 DOM 没被重建
- [ ] 4.4 `make-fixture` 加一页双镜样例；V13/V13b 对无 shots 的包读数不变

## 5. 2x 素材（后置，只影响清晰度）

- [ ] 5.1 对声明 `Crop` 的页，导出时 `RenderOptions{DPI:300}` 重渲一次整页
- [ ] 5.2 按 `Crop` 裁切并作为 `Shot.Image` 落盘；同页多个特写共用一次渲染
- [ ] 5.3 验收用可量的判据：特写图边缘梯度能量 ≥ 300 DPI 源裁切的 95%（不看「看起来清楚了吗」）

## 6. 文档

- [ ] 6.1 `render-plan` 规格补 Shot 语义、三条校验与「crop 镜头内层不显示」
- [ ] 6.2 `html-share-bundle` 规格补清单字段、schemaVersion 3 与「源页」口径
- [ ] 6.3 路线图补一行，写明它与 G7/G8 的边界
