package share

import (
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
