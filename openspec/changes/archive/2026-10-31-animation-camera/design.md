# 设计：镜头相机（Shot.Camera）

> 三条设计取舍已拍板：**相机增强特写（crop），不独立作用于整页**、**坐标用镜头图局部归一化**、
> **无相机 = 当前行为**。本文件把这三条例成可执行契约，然后才写代码。

## 1. 数据模型

相机关键帧是「镜头图局部的归一化坐标」，与具体分辨率无关：

```go
// render/camera.go（新增）
type CameraKeyframe struct {
    AtMs  int64   `json:"atMs"`  // 镜内相对毫秒，MUST 升序
    X     float64 `json:"x"`     // 视口中心 X（归一化到镜头图，0.5=中心）；合法值 MUST 落在覆盖安全区间 [0.5/scale,1-0.5/scale]
    Y     float64 `json:"y"`     // 视口中心 Y，同上
    Scale float64 `json:"scale"` // 相对「图覆盖舞台」的缩放；MUST ≥ 1
}
```

挂载点（三处对齐，与现有 `crop` 同构）：

| 层 | 类型 | 字段 |
|---|---|---|
| 规格层 | `animation.Revision.Shots[].Camera []CameraKeyframe` | 新增（作者用归一化坐标） |
| 应用层 | `render.PageShotInput.Shots[].Camera` / `render.Shot.Camera` | 新增 |
| 打包层 | `share.SlideShot.Camera []CameraKeyframeShare` | 新增 |
| 浏览器 | `shared/types.ts` `Shot.camera?: CameraKeyframe[]` | 新增 |
| 清单/产物 | `SlideShot` + `camera` 可选字段 | 新增 |

`camera` 与 `crop` 的关系（v1 硬规则）：**声明了 `camera` 的镜头 MUST 同时声明 `crop`**。
相机在特写图内部运动；无 `crop` 的整页相机留到后续 change。

## 2. 坐标系统与播放器变换数学

镜头图元素默认以 `object-fit: cover` 铺满舞台（即 scale=1 时图恰好覆盖舞台）。
相机要把镜头图的归一化视口中心 `(X, Y)` 移到舞台中心，并放大 `scale` 倍。

以舞台局部像素（未乘 `--ppts-k` 响应式系数）表达，设舞台逻辑尺寸 `videoW × videoH`、
`transform-origin: center`：

```
tx = (0.5 - X) * videoW * scale
ty = (0.5 - Y) * videoH * scale
transform: translate(calc(tx px * var(--ppts-k,1)), calc(ty px * var(--ppts-k,1))) scale(scale)
```

**覆盖不变量（校验核心，初版曾写错）**：图以 `transform-origin: center` 居中后尺寸为
`videoW*scale × videoH*scale`，再平移 `tx=(0.5-X)*videoW*scale`、`ty=(0.5-Y)*videoH*scale`。
左边缘在屏幕上的位置 = `videoW*(0.5 - X*scale)`。要不露白，需左边缘 ≤ 0 且右边缘 ≥ videoW，
即 `X ∈ [0.5/scale, 1-0.5/scale]`，Y 同理。**单纯 `X,Y ∈ [0,1]` 不足以保证不露白**——
例：`scale=2, X=0` 时左半边舞台会露白（左边缘 = `videoW*0.5 > 0`）。故合法中心 MUST 落在
`[0.5/scale, 1-0.5/scale]`（scale=1 退化为单点 0.5，此时不能平移），**该区间才是校验项，
`[0,1]` 只是归一化存储范围**。校验拒绝区间外关键帧，`CameraTransform` 再钳制一次作防御兜底。

反例：若 `scale < 1`，图本就小于舞台，必露白；若 `scale ≥ 1` 但中心越界（如 `scale=2, X=0`），
同样露白。两者都是 MUST 校验项（见 §6）。

与现有转场一致：平移量乘 `var(--ppts-k,1)`（见 `main.ts:466` 的 `translateX(... * var(--ppts-k))`），
保证响应式缩放下相机位移比例正确。

## 3. IR 编译

`compileShots`（已有）在把 EMU 镜头换算成画布像素 `Box` 的同时，**原样透传 `camera` 关键帧**——
它们是归一化坐标，已是后端无关，无需 EMU→画布换算。`PageShotInput.Shots[].Camera` 直接拷到
`Page.Shots[].Camera`。

## 4. 播放器集成

`syncShot`（`main.ts:348`）已按 `shotAt` 选镜并设 `cur.img.src = frame.shot.image`。改动：

1. 舞台 `.ppts-stage` 增加 `overflow: hidden`（已有 `aspect-ratio`，裁剪靠它）。
2. 选中镜头后，若 `frame.shot.camera` 非空，按 `(localMs - shot.startMs)` 调相机插值纯函数
   得到 `{x, y, scale}`，把 §2 的 `transform` 写到 `cur.img`（及叠化期的 `imgTop`）上；
   否则不写 transform（当前行为）。
3. 相机镜头沿用「`crop` 非空 → 隐藏层」规则（`main.ts:380`），不引入层跟随。

相机插值为纯函数（浏览器 `shared/shots.ts` + Go 侧同语义），**相邻关键帧线性插值**，
`localMs` 早于首帧钳制到首帧、晚于末帧钳制到末帧；`scale` 取插值结果并 `max(1, ·)` 兜底。

## 5. 与 300 DPI 重渲的关系（诚实声明）

`animation-page-shots` 的 2x 路径已经对**有 `crop` 的镜头**整页 300 DPI 重渲并裁出特写。
v1 相机镜头 MUST 同时有 `crop`，因此天然复用 300 DPI 特写，相机放大不会立刻糊。

**v1 不改 300 DPI 触发逻辑**。已知代价：若某相机镜头 `scale` 远大于 1（如特写上再推近 3 倍），
而特写源自 300 DPI 裁切（放大倍率有限），极端放大仍会糊——这是「清晰度 vs 体积」的已知权衡，
留给后续「按 `maxScale` 自适应提升裁切 DPI」的 refinement，不在本变更 scope。

## 6. 校验门禁

- **新增 V 项（相机边界）**：随机抽镜头，断言 `scale ≥ 1`、插值结果无 NaN、覆盖不变量成立
  （变换后镜头图四边不内缩于舞台）。Go 侧相机插值纯函数单测固定该语义。
- **V13/V13b/V16/V17/V18 不受影响**：它们都是「切镜 / 层重建」判据；相机是镜内运动，不触发切镜、
  不重建层（层在相机镜头下本就隐藏），取样点不变。
- **`bundle-gate.py` / `shots-check.mjs` 容忍新字段**：`camera` 是可选字段，旧播放器忽略它停在
  首镜（`slide.image` 已绑死为 `shots[0].image`），与 `shots` 的降级语义一致；门禁 schema 校验
  MUST 把 `camera` 列为可选、不破坏既有逐字节一致性断言。
- **golden 样本**：在 `share-baseline` 夹具加一条带相机的镜头（`-shots` 路径已存在），确保
  相机镜头也被预算校验与门禁覆盖。

## 7. 明确不做（留给后续独立 change）

- G8 跨场景元素延续（改时间基准，两个量级）。
- 相机作用于整页镜头 + 层跟随（需层随底图 transform）。
- 「跟随」运动（需逐帧元素世界坐标）。
- 帧渲染器（MP4）侧相机合成（IR 携带，渲染器未实现时退化为静态特写）。
