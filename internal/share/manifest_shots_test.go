package share

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/render"
)

// shotsManifest 造一份最小可校验清单：一页 6s，给定镜头切片。
func shotsManifest(shots ...SlideShot) *Manifest {
	return &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Artifact:      ArtifactInfo{DurationUS: 6_000_000},
		Video:         VideoInfo{Width: 1920, Height: 1080, FPS: 30},
		Slides: []SlideEntry{{
			SlideID: "slide-1", StartUS: 0, EndUS: 6_000_000,
			Image: "pages/page_01.png", Shots: shots,
		}},
		Subtitles: SubtitleInfo{Src: "subtitles.vtt", Inline: "subtitles.js"},
	}
}

func TestSlideShotsValid(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000, CrossFadeMs: 0},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop:    &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000, CrossFadeMs: 120},
	)
	if err := m.Validate(nil); err != nil {
		t.Fatalf("valid shots rejected: %v", err)
	}
}

func TestSlideShotsRejectGap(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01b.png", StartMs: 3100, DurationMs: 2900},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "gaps/overlaps") {
		t.Fatalf("want gap error, got %v", err)
	}
}

func TestSlideShotsRejectCoverage(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01b.png", StartMs: 3000, DurationMs: 2000},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "shots cover") {
		t.Fatalf("want coverage error, got %v", err)
	}
}

func TestSlideShotsRejectFirstImageMismatch(t *testing.T) {
	// 旧播放器只认 slide.image；两者不一致时同一份包会给出两个画面。
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/other.png", StartMs: 0, DurationMs: 6000},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "must equal slide image") {
		t.Fatalf("want first-image error, got %v", err)
	}
}

func TestSlideShotsRejectMissingFile(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/gone.png", StartMs: 3000, DurationMs: 3000},
	)
	files := map[string]struct{}{"pages/page_01.png": {}}
	err := m.Validate(files)
	if err == nil || !strings.Contains(err.Error(), "missing from bundle") {
		t.Fatalf("want missing-file error, got %v", err)
	}
}

func TestSlideShotsRejectCropOutside(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/zoom.png", Crop: &SlideBox{X: 1800, Y: 100, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "outside page") {
		t.Fatalf("want crop bounds error, got %v", err)
	}
}

// share 不依赖 render（依赖纪律），镜头数上限因此在两边各写一份字面量。
// 这条测试是它们之间唯一的桥：改一边不改另一边就红。
func TestMaxShotsMatchesIR(t *testing.T) {
	if maxShotsPerSlide != render.MaxShotsPerPage {
		t.Fatalf("share.maxShotsPerSlide=%d, render.MaxShotsPerPage=%d（两边必须同值）",
			maxShotsPerSlide, render.MaxShotsPerPage)
	}
}

// ---- 相机（Shot.Camera）门禁：产物侧硬拒绝非法中心 ----

func TestSlideShotsCameraValid(t *testing.T) {
	// scale=2，中心落在覆盖安全区间 [0.25,0.75] 内 → 通过。
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000, CrossFadeMs: 0},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop:    &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000, CrossFadeMs: 120,
			Camera: []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 2}}},
	)
	if err := m.Validate(nil); err != nil {
		t.Fatalf("valid camera rejected: %v", err)
	}
}

func TestSlideShotsRejectCameraCenterOutsideSafe(t *testing.T) {
	// scale=2，安全区间 [0.25,0.75]；X=0 越界 → 拒绝（单纯 x∈[0,1] 不足以保证不露白）。
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop:    &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000,
			Camera: []CameraKeyframe{{AtMs: 0, X: 0, Y: 0.5, Scale: 2}}},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "coverage-safe range") {
		t.Fatalf("want coverage-safe-range error, got %v", err)
	}
}

func TestSlideShotsRejectCameraScaleLT1(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop:    &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000,
			Camera: []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 0.5}}},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "scale") {
		t.Fatalf("want scale error, got %v", err)
	}
}

func TestSlideShotsRejectCameraNoCrop(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			StartMs: 3000, DurationMs: 3000,
			Camera: []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 2}}},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "no crop") {
		t.Fatalf("want camera-without-crop error, got %v", err)
	}
}

func TestSlideShotsRejectCameraOnFirstShot(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000,
			Camera: []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 1}}},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "must not declare a camera") {
		t.Fatalf("want first-shot-camera error, got %v", err)
	}
}

func TestSlideShotsRejectCameraAtMsNotAscending(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop:    &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000,
			Camera: []CameraKeyframe{
				{AtMs: 1000, X: 0.5, Y: 0.5, Scale: 2},
				{AtMs: 0, X: 0.5, Y: 0.5, Scale: 2},
			}},
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "ascending") {
		t.Fatalf("want ascending-atMs error, got %v", err)
	}
}

func TestSlideShotsRejectCameraNaN(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop:    &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000,
			Camera: []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 2},
				{AtMs: 1000, X: 0.5, Y: 0.5, Scale: math.NaN()}}}, // NaN
	)
	err := m.Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("want NaN-coordinate error, got %v", err)
	}
}

// TestSlideShotsNoCameraByteIdentical 守住「可选字段」承诺：没有相机的包，序列化后
// MUST 不含 camera 键——否则门禁的逐字节一致性断言会悄悄破掉。
func TestSlideShotsNoCameraByteIdentical(t *testing.T) {
	m := shotsManifest(
		SlideShot{Index: 0, Image: "pages/page_01.png", StartMs: 0, DurationMs: 3000, CrossFadeMs: 0},
		SlideShot{Index: 1, Image: "pages/page_01_zoom.png",
			Crop: &SlideBox{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000, CrossFadeMs: 120},
	)
	raw, err := json.Marshal(m.Slides[0].Shots)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "camera") {
		t.Fatalf("shots without camera must not serialize the field, got %s", raw)
	}
}
