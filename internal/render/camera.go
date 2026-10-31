package render

// CameraKeyframe 是镜头图局部的归一化相机关键帧。
//
// 坐标与具体分辨率无关：x/y 是视口中心在镜头图上的归一化位置，scale ≥ 1 是相对
// 「图覆盖舞台」的缩放倍率。相邻关键帧间线性插值（首帧前/末帧后钳制到端点）。
//
// x/y 的安全区间由 scale 推导（见 CameraTransform 内的 clampCenter）：要让缩放后的镜头图
// 始终盖住舞台、不露白，视口中心 MUST 落在 [0.5/scale, 1-0.5/scale]。scale=1 时该区间
// 退化为单点 0.5（不能平移）；scale=2 时为 [0.25, 0.75]。这是与浏览器 shared/shots.ts
// 的 cameraTransform 同语义的约束。
//
// 相机仅在「带 crop 的特写」上运动（见 animation-camera design.md），因此本结构不表达
// 整页相机；整页相机 + 层跟随留给后续 change。
type CameraKeyframe struct {
	AtMs  int64   `json:"atMs"`  // 镜内相对毫秒，MUST 升序
	X     float64 `json:"x"`     // 视口中心 X（归一化到镜头图，安全区间见 CameraTransform）
	Y     float64 `json:"y"`     // 视口中心 Y，同上
	Scale float64 `json:"scale"` // 相对「图覆盖舞台」的缩放；MUST ≥ 1
}

// CameraTransform 在镜内相对时间 shotLocalMs 处插值相机变换，返回视口中心 (x, y) 与 scale。
//
// 关键帧 MUST 已按 AtMs 升序。早于首帧钳制到首帧、晚于末帧钳制到末帧；scale 取 max(1, ·)
// 兜底（scale < 1 会让镜头图盖不住舞台、边缘露白，见 animation-camera spec）。x/y 经
// clampCenter 钳制到覆盖安全区间，保证渲染出的变换永远不露白（即便输入越界，也只裁掉
// 越界部分而非露出空白）。
func CameraTransform(kfs []CameraKeyframe, shotLocalMs int64) (x, y, scale float64) {
	if len(kfs) == 0 {
		return 0.5, 0.5, 1
	}
	if shotLocalMs <= kfs[0].AtMs {
		k := kfs[0]
		return clampCenter(k.X, k.Y, clampScaleGE1(k.Scale))
	}
	last := kfs[len(kfs)-1]
	if shotLocalMs >= last.AtMs {
		return clampCenter(last.X, last.Y, clampScaleGE1(last.Scale))
	}
	for i := 1; i < len(kfs); i++ {
		if shotLocalMs <= kfs[i].AtMs {
			a, b := kfs[i-1], kfs[i]
			t := float64(shotLocalMs-a.AtMs) / float64(b.AtMs-a.AtMs)
			s := clampScaleGE1(lerpF(a.Scale, b.Scale, t))
			return clampCenter(lerpF(a.X, b.X, t), lerpF(a.Y, b.Y, t), s)
		}
	}
	return clampCenter(last.X, last.Y, clampScaleGE1(last.Scale))
}

// clampCenter 把视口中心钳制到覆盖安全区间 [0.5/scale, 1-0.5/scale]。
// 推导：缩放后镜头图宽为 stageW*scale，其左边缘在屏幕上的位置为 stageW/2 - stageW*scale*x
// （transform 以中心为原点、translate 为 (0.5-x)*stageW*scale）。要左边缘 ≤ 0 且右边缘 ≥
// stageW，需 x ∈ [0.5/scale, 1-0.5/scale]，否则舞台会露白。
func clampCenter(x, y, scale float64) (float64, float64, float64) {
	if scale <= 0 {
		scale = 1
	}
	lo, hi := 0.5/scale, 1-0.5/scale
	return clampF(x, lo, hi), clampF(y, lo, hi), scale
}

func lerpF(a, b, t float64) float64 { return a + (b-a)*t }

func clampF(v, lo, hi float64) float64 {
	// v != v 是 NaN 的惯用判据：NaN 既不小于 lo 也不大于 hi，会原样返回 NaN，
	// 进而污染 transform 让舞台崩。这里把 NaN 当越界钳到 lo，保证输出永远有限。
	if v < lo || v != v {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampScaleGE1(v float64) float64 {
	if v < 1 {
		return 1
	}
	if v != v { // NaN 兜底：保持 1，避免变换写出 NaN 让舞台崩
		return 1
	}
	return v
}
