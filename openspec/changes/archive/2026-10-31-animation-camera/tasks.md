# 任务：镜头相机（Shot.Camera）

> 交付纪律：本变更对应设计方案 §19 批次三的 G7。纯增量、可选字段、缺省行为不变。
> 合并门禁：`go build ./...`、`go vet ./...`、`go test ./...`（含相机插值 Go 单测）、
> `tsc -b`、`vite build`（含 `internal/share/assets/app.css` 同步重建）、相机插值纯函数单测
> （与 Go 侧同语义）。一个里程碑 = 一次 commit + push + 全量验证。

## 0. 侦察（已部分完成）
- [x] 0.1 确认 `Shot` 在 `internal/render/ir.go` / `internal/share/manifest.go:82 SlideShot` /
  `shared/types.ts` 三处对齐，`shotAt` 为浏览器/合成器共用选镜函数
- [x] 0.2 确认播放器 `syncShot` 已按镜切底图、有 `crop` 时隐藏层、舞台有 `--ppts-k` 响应式系数
- [x] 0.3 确认 `compileShots` 已存在、相机为归一化坐标无需 EMU 换算

## 1. 数据模型（三处对齐）
- [x] 1.1 新增 `render/camera.go`：`CameraKeyframe{AtMs, X, Y, Scale}`，`Scale ≥ 1` 注释与校验
- [x] 1.2 `render.PageShotInput.Shots[]` 与 `render.Shot` 增 `Camera []CameraKeyframe`
- [x] 1.3 `animation.Revision.Shots[]` 增 `Camera`（作者用归一化坐标）
- [x] 1.4 `internal/app/animation_pages.go` 的 `revShots` 透传 `Camera`
- [x] 1.5 `share.ShotAsset`（bundle.go 内部）与 `share.SlideShot`（manifest.go 序列化）均增
  `Camera []CameraKeyframe`（同一类型，与 `render` 同构）；`writeShots` 透传；
  `validateSlideShots`（manifest.go:302）扩展校验：`scale ≥ 1`、**中心落在覆盖安全区间 [0.5/scale,1-0.5/scale]**、
  关键帧按 `atMs` 升序、相机镜头 MUST 带 `crop`（注：`[0,1]` 只是归一化存储范围，非安全区间）
- [x] 1.6 `shared/types.ts` `Shot` 增 `camera?: CameraKeyframe[]`；`shared/shots.ts` 导出
  `CameraKeyframe` 类型与相机插值纯函数 `cameraTransform(kfs, shotLocalMs)`

## 2. IR 编译
- [x] 2.1 `compileShots` 原样透传 `Camera`（归一化，不换算）

## 3. 播放器集成
- [x] 3.1 `.ppts-stage` 增加 `overflow: hidden`
- [x] 3.2 `syncShot` 在选中镜头后，若有 `camera` 则按 `(localMs - shot.startMs)` 调
  `cameraTransform` 并写 `transform` 到 `cur.img` / `imgTop`
- [x] 3.3 无 `camera` 时不写 transform（当前行为）；相机镜头沿用「crop 非空 → 隐藏层」

## 4. 校验门禁
- [x] 4.1 Go 侧相机插值纯函数 + 单测（端点钳制、线性插值、`scale ≥ 1` 兜底、无 NaN）
- [x] 4.2 浏览器 `cameraTransform` 单测（与 Go 同语义；`shots-check.mjs` 或新增 `camera-check.mjs`）
- [x] 4.3 新增 V 项：相机覆盖不变量（变换后镜头图四边不内缩于舞台）
- [x] 4.4 `bundle-gate.py` / `shots-check.mjs` 把 `camera` 列为可选字段，不破坏逐字节一致性断言
- [x] 4.5 `share-baseline` 夹具加一条带相机的镜头（`-shots` 路径），覆盖预算校验与门禁

## 5. 收尾
- [x] 5.1 路线图补一行，写明 G7 与 G8 的边界（G8 跨场景元素延续仍独立）
- [x] 5.2 `go build/vet/test` + `tsc -b` + `vite build`（同步 `internal/share/assets/app.css`）
- [x] 5.3 `openspec archive animation-camera` + CI 不受影响确认 + 提交推送
