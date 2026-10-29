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
- [ ] 2.3 两侧构建产物确认引用同一份（渲染器 `tsc` 与 web `vite build:share` 各跑一遍），
      并删掉任何残留的重复实现 —— 待组 4 播放器真正 import 后再验证

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

- [ ] 4.1 编译：`clips` → WAAPI keyframes（属性展开、重叠合并采样、缓动序列化），
      编译结果缓存在内存，不写回清单
- [ ] 4.2 回放：`play/pause/seek` 与 WAAPI 同步；seek 先批量 `pause()` 设 `currentTime` 再按状态 `play()`
- [ ] 4.3 切页时 `cancel()` 上一页全部 animation（不是 `pause()`——留着会占合成层）
- [ ] 4.4 时钟跟 `audio.currentTime`，不引入 rAF 自走
- [ ] 4.5 无 WAAPI 降级：删掉 `Element.animate` 后仍能播（静态切页），不抛错
- [ ] 4.6 `prefers-reduced-motion: reduce`：保留 opacity、去掉位移缩放、**时长不变**（否则音画错位）

## 5. 门禁工具与验收

- [ ] 5.1 SSIM 工具：MP4 取帧（`ffmpeg -ss`）+ 无头 Chrome 打开成品包 pause 到同一 `currentTime` 截图，
      算 SSIM；复用 `scripts/review/compare.py` 的灰度/分窗逻辑，不引 skimage
- [ ] 5.2 取帧时点：每页**第一条 clip 的中点**（运动进行中）+ 首帧 + 末帧；
      只取末帧等于什么都没验（design ADR-7）
- [ ] 5.3 逐页**最差帧** ≥ 0.95，不取全片平均（平均会把一页的失败摊薄到 20 页里）
- [ ] 5.4 静态回归：关闭动画开关的包与 M6-1 产物逐像素一致
- [ ] 5.5 体积基线写进 `deploy/` 或包 README：5 分钟 / 20 页的实测值

## 6. 文档与收口

- [ ] 6.1 `README` 补分层位图格式、体积代价、降级行为
- [ ] 6.2 路线图第 1 节表格更新 `html-bundle-animation` 状态；`animation-render-service-image`
      的 2.9 勾掉并注明「由 M6-2 的共享 `shared/motion.ts` 落实」
- [ ] 6.3 `openspec validate --all` 通过后归档，delta 吸收进 `openspec/specs/html-share-bundle`
