package render

import (
	"math"
	"testing"
)

// 相机插值纯函数（CameraTransform）的语义固定：端点钳制、线性插值、scale≥1 兜底、NaN 安全，
// 以及「覆盖不变量」——输出中心 MUST 落在 [0.5/scale, 1-0.5/scale]，保证变换后镜头图不露白。
//
// 这是 G7 的相机覆盖 V 项（见 animation-camera design.md §6）的 durable 保证：校验层（compile /
// manifest）拒绝非法中心，运行时 CameraTransform 再钳制一次兜底，两端（Go 合成器 + 浏览器播放器）
// 共用同一份语义。本文件把这套语义钉死。

func TestCameraTransformEndpoints(t *testing.T) {
	kfs := []CameraKeyframe{
		{AtMs: 0, X: 0.5, Y: 0.5, Scale: 1},
		{AtMs: 1000, X: 0.5, Y: 0.5, Scale: 2},
	}
	// 早于首帧 → 钳到首帧
	x, y, s := CameraTransform(kfs, -100)
	if x != 0.5 || y != 0.5 || s != 1 {
		t.Fatalf("before first: got (%v,%v,%v), want (0.5,0.5,1)", x, y, s)
	}
	// 晚于末帧 → 钳到末帧
	x, y, s = CameraTransform(kfs, 99999)
	if x != 0.5 || y != 0.5 || s != 2 {
		t.Fatalf("after last: got (%v,%v,%v), want (0.5,0.5,2)", x, y, s)
	}
}

func TestCameraTransformInterp(t *testing.T) {
	// scale=2（安全区间 [0.25,0.75]），中心从 (0.3,0.4) 线性走到 (0.7,0.6)。
	// 两端都在安全区间内，插值途中也不越界，故全程不钳制。
	kfs := []CameraKeyframe{
		{AtMs: 0, X: 0.3, Y: 0.4, Scale: 2},
		{AtMs: 1000, X: 0.7, Y: 0.6, Scale: 2},
	}
	x, y, s := CameraTransform(kfs, 500) // 中点
	if math.Abs(x-0.5) > 1e-9 || math.Abs(y-0.5) > 1e-9 {
		t.Fatalf("midpoint interp: got (%v,%v), want (0.5,0.5)", x, y)
	}
	if math.Abs(s-2) > 1e-9 {
		t.Fatalf("midpoint scale: got %v, want 2", s)
	}
}

func TestCameraTransformScaleFloor(t *testing.T) {
	kfs := []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 0.5}}
	x, y, s := CameraTransform(kfs, 0)
	if s != 1 {
		t.Fatalf("scale<1 must floor to 1, got %v", s)
	}
	if x != 0.5 || y != 0.5 {
		t.Fatalf("valid center passes through, got (%v,%v)", x, y)
	}
}

func TestCameraTransformNaNScale(t *testing.T) {
	kfs := []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: math.NaN()}}
	x, y, s := CameraTransform(kfs, 0)
	if math.IsNaN(s) || math.IsNaN(x) || math.IsNaN(y) {
		t.Fatalf("NaN scale must not propagate: got (%v,%v,%v)", x, y, s)
	}
	if s != 1 {
		t.Fatalf("NaN scale floors to 1, got %v", s)
	}
}

func TestCameraTransformNaNCoordinate(t *testing.T) {
	// NaN 坐标（如手改清单）MUST 被钳到安全区间而非透传：运行时防御，与 clampF 一致。
	kfs := []CameraKeyframe{{AtMs: 0, X: math.NaN(), Y: math.NaN(), Scale: 2}}
	x, y, s := CameraTransform(kfs, 0)
	if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(s) {
		t.Fatalf("NaN coordinate must be clamped, got (%v,%v,%v)", x, y, s)
	}
	// scale=2 安全区间 [0.25,0.75]；NaN 钳到 lo=0.25。
	if x != 0.25 || y != 0.25 || s != 2 {
		t.Fatalf("NaN coordinate clamped to lo: got (%v,%v,%v), want (0.25,0.25,2)", x, y, s)
	}
}

// TestCameraCoverageInvariant 是覆盖不变量定理（G7 相机 V 项的核心）：无论输入中心如何越界，
// CameraTransform 的输出 MUST 落在覆盖安全区间，从而变换后镜头图四边不内缩于舞台。
//
// 推导（design §2）：以 transform-origin:center 居中后图尺寸 videoW*scale × videoH*scale，
// 平移 tx=(0.5-X)*videoW*scale。左边缘 = videoW*(0.5 - X*scale)，右边缘 = videoW*(0.5 + scale*(1-X))。
// 不露白 ⇔ 左≤0 且 右≥videoW ⇔ X∈[0.5/scale, 1-0.5/scale]。Y 同理。
func TestCameraCoverageInvariant(t *testing.T) {
	const vW, vH = 1920.0, 1080.0
	scales := []float64{1, 1.5, 2, 3, 4}
	for _, sc := range scales {
		safe := sc
		if safe < 1 {
			safe = 1
		}
		lo, hi := 0.5/safe, 1-0.5/safe
		// 故意喂越界中心（0→1 扫过），验证输出被钳到安全区间且变换后图盖住舞台。
		kfs := []CameraKeyframe{
			{AtMs: 0, X: 0, Y: 0, Scale: sc},
			{AtMs: 1000, X: 1, Y: 1, Scale: sc},
		}
		for ms := int64(0); ms <= 1000; ms += 25 {
			x, y, s := CameraTransform(kfs, ms)
			if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(s) {
				t.Fatalf("scale=%v ms=%d: output has NaN (%v,%v,%v)", sc, ms, x, y, s)
			}
			if x < lo-1e-9 || x > hi+1e-9 {
				t.Fatalf("scale=%v ms=%d: x=%v outside safe [%v,%v]", sc, ms, x, lo, hi)
			}
			if y < lo-1e-9 || y > hi+1e-9 {
				t.Fatalf("scale=%v ms=%d: y=%v outside safe [%v,%v]", sc, ms, y, lo, hi)
			}
			left := vW * (0.5 - x*s)
			right := vW * (0.5 + s*(1-x))
			top := vH * (0.5 - y*s)
			bottom := vH * (0.5 + s*(1-y))
			if left > 1e-6 || right < vW-1e-6 || top > 1e-6 || bottom < vH-1e-6 {
				t.Fatalf("scale=%v ms=%d: transform exposes stage edges (left=%v right=%v top=%v bottom=%v)",
					sc, ms, left, right, top, bottom)
			}
		}
	}
}

// TestCameraValidKeyframeUnclamped 验证落在安全区间内的合法关键帧不被钳制（行为不变）。
func TestCameraValidKeyframeUnclamped(t *testing.T) {
	// scale=2，安全区间 [0.25,0.75]，两端取边界值，插值途中的点也应恰好在边界上。
	kfs := []CameraKeyframe{
		{AtMs: 0, X: 0.25, Y: 0.75, Scale: 2},
		{AtMs: 1000, X: 0.75, Y: 0.25, Scale: 2},
	}
	for _, ms := range []int64{0, 250, 500, 750, 1000} {
		x, y, s := CameraTransform(kfs, ms)
		if s != 2 {
			t.Fatalf("ms=%d: scale should stay 2, got %v", ms, s)
		}
		if math.Abs(x-0.25) > 1e-9 || math.Abs(x-0.75) > 1e-9 {
			// x 在 0.25..0.75 之间线性变化，必在 [0.25,0.75] 内。
			if x < 0.25-1e-9 || x > 0.75+1e-9 {
				t.Fatalf("ms=%d: x=%v should stay within [0.25,0.75]", ms, x)
			}
		}
		if math.Abs(y-0.75) > 1e-9 || math.Abs(y-0.25) > 1e-9 {
			if y < 0.25-1e-9 || y > 0.75+1e-9 {
				t.Fatalf("ms=%d: y=%v should stay within [0.25,0.75]", ms, y)
			}
		}
	}
}
