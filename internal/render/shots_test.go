package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// shotPlan 在 validPlan 的首页上挂一组镜头切片。
// 首镜必须沿用页级 baseImage（pages/p0.png）——那条约束由 validateShots 守。
func shotPlan(shots ...Shot) *Timeline {
	p := validPlan()
	p.Pages[0].Shots = shots
	return p
}

func TestShotsOmittedWhenEmpty(t *testing.T) {
	// 没有镜头切片的包必须与今天逐字节一致：字段带 omitempty 还不够，
	// 要真的 marshal 一次看字节——否则「加了个空数组」这种退化会在门禁之外悄悄发生。
	raw, err := json.Marshal(validPlan().Pages[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "shots") {
		t.Fatalf("page without shots must not serialize the field, got %s", raw)
	}
}

func TestShotsValid(t *testing.T) {
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000, CrossFadeMs: 0},
		// 特写镜头：裁切区域 800×450，宽高比与整页 16:9 一致。
		Shot{Index: 1, Image: "pages/p0_zoom.png", Crop: &Box{X: 200, Y: 150, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000, CrossFadeMs: 120},
	)
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid shots rejected: %v", err)
	}
}

func TestShotsRejectGap(t *testing.T) {
	// 100ms 的空洞意味着这 100ms 没有图。画面会停在上一镜，不报错、只是不动。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000},
		Shot{Index: 1, Image: "pages/p0b.png", StartMs: 3100, DurationMs: 2900},
	)
	err := plan.Validate()
	if err == nil {
		t.Fatal("gap between shots must be rejected")
	}
	if !strings.Contains(err.Error(), "shots[1].startMs") {
		t.Fatalf("error should locate the gap, got %v", err)
	}
}

func TestShotsRejectCoverageMismatch(t *testing.T) {
	// 少 1000ms：页尾没有图可显示。多出来同理。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000},
		Shot{Index: 1, Image: "pages/p0b.png", StartMs: 3000, DurationMs: 2000},
	)
	err := plan.Validate()
	if err == nil {
		t.Fatal("shots covering less than the page must be rejected")
	}
	if !strings.Contains(err.Error(), "shots cover") {
		t.Fatalf("error should state the covered span, got %v", err)
	}
}

func TestShotsRejectTooMany(t *testing.T) {
	// 6 个镜头、每个 1000ms，时间轴是连续的——只有数量越界。
	shots := make([]Shot, 0, 6)
	for i := 0; i < 6; i++ {
		img := "pages/p0.png"
		if i > 0 {
			img = "pages/p0b.png"
		}
		shots = append(shots, Shot{Index: i, Image: img, StartMs: int64(i) * 1000, DurationMs: 1000})
	}
	plan := shotPlan(shots...)
	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("want shot count limit error, got %v", err)
	}
}

func TestShotsRejectCrossFadeLongerThanShot(t *testing.T) {
	// 叠化 800ms 而本镜只有 600ms：这一镜还没淡完就结束了，画面停在半混合态。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 5400},
		Shot{Index: 1, Image: "pages/p0b.png", StartMs: 5400, DurationMs: 600, CrossFadeMs: 800},
	)
	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "crossFade") {
		t.Fatalf("want crossFade error, got %v", err)
	}
}

func TestShotsRejectCropOutsidePage(t *testing.T) {
	// x+w = 2600 > 1920：裁到页面外了（多出来的部分是透明或黑边）。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000},
		Shot{Index: 1, Image: "pages/p0_zoom.png", Crop: &Box{X: 1800, Y: 100, W: 800, H: 450},
			StartMs: 3000, DurationMs: 3000},
	)
	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "outside page") {
		t.Fatalf("want crop bounds error, got %v", err)
	}
}

func TestShotsFirstImageMustMatchBase(t *testing.T) {
	// 首镜换了张图：页级 image 与首镜给了两个说法，播放器与合成器会各取一个。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/other.png", StartMs: 0, DurationMs: 6000},
	)
	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "must equal the page's display base") {
		t.Fatalf("want first-shot/base mismatch, got %v", err)
	}
}

func TestShotsFirstImageIsPlateWhenPageHasOne(t *testing.T) {
	// 有分层动画的页，页级图是**底板**（元素被挖空、由 layers 盖回去）。
	// 首镜若仍写 baseImage，元素会既在图里、又从零淡入一次——重影，且两边都不报错。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 6000},
	)
	plan.Pages[0].PlateImage = "pages/p0-plate.png"
	if err := plan.Validate(); err == nil {
		t.Fatal("first shot pointing at baseImage on a plated page must be rejected")
	}

	// 指到底板就对了。
	plan.Pages[0].Shots[0].Image = "pages/p0-plate.png"
	if err := plan.Validate(); err != nil {
		t.Fatalf("first shot pointing at the plate must pass: %v", err)
	}
}

func TestShotsIndexMustMatchPosition(t *testing.T) {
	// index 与位置不一致：排序有两个答案，后续按 index 索引的地方会取错图。
	plan := shotPlan(
		Shot{Index: 0, Image: "pages/p0.png", StartMs: 0, DurationMs: 3000},
		Shot{Index: 5, Image: "pages/p0b.png", StartMs: 3000, DurationMs: 3000},
	)
	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "does not match position") {
		t.Fatalf("want index/position error, got %v", err)
	}
}

// hasDiag 已在 compile_test.go 中定义，这里直接复用。

func TestCompileShotsCameraValid(t *testing.T) {
	// 合法相机（scale=2，中心在 [0.25,0.75] 内）不应产生 DiagCameraInvalid。
	c := newTestCompiler(t)
	page := &Page{Index: 0, SlideID: "s", DurationMs: 6000, BaseImage: "pages/p0.png"}
	shots := []PageShotInput{
		{StartMs: 0, DurationMs: 3000},
		{StartMs: 3000, DurationMs: 3000, Crop: emuBox(200, 150, 800, 450),
			Camera: []CameraKeyframe{{AtMs: 0, X: 0.5, Y: 0.5, Scale: 2}}},
	}
	_, diags := c.compileShots(PageInput{Index: 0, SlideID: "s", DurationMs: 6000,
		BaseImage: "pages/p0.png", Shots: shots}, page)
	if hasDiag(diags, DiagCameraInvalid) {
		t.Fatalf("valid camera must not emit DiagCameraInvalid, got %v", diags)
	}
}

func TestCompileShotsCameraDiagnostics(t *testing.T) {
	// 越界相机（scale=2，X=0 落在安全区间 [0.25,0.75] 之外）→ 诊断。
	// IR 侧只留痕（诊断），硬拒绝在 manifest（产物）层；两端规则一致。
	c := newTestCompiler(t)
	page := &Page{Index: 0, SlideID: "s", DurationMs: 6000, BaseImage: "pages/p0.png"}
	shots := []PageShotInput{
		{StartMs: 0, DurationMs: 3000},
		{StartMs: 3000, DurationMs: 3000, Crop: emuBox(200, 150, 800, 450),
			Camera: []CameraKeyframe{{AtMs: 0, X: 0, Y: 0.5, Scale: 2}}},
	}
	out, diags := c.compileShots(PageInput{Index: 0, SlideID: "s", DurationMs: 6000,
		BaseImage: "pages/p0.png", Shots: shots}, page)
	if len(out) != 2 {
		t.Fatalf("want 2 shots, got %d", len(out))
	}
	if !hasDiag(diags, DiagCameraInvalid) {
		t.Fatalf("want DiagCameraInvalid for out-of-range camera, got %v", diags)
	}
}
