package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func px(x, y, w, h int64) *EMUBox {
	return &EMUBox{X: x, Y: y, Width: w, Height: h}
}

func TestClassifyGazeLayoutGridListFigure(t *testing.T) {
	const cw, ch = 1920, 1080
	grid := make([]Element, 0, 6)
	for row := 0; row < 2; row++ {
		for col := 0; col < 3; col++ {
			grid = append(grid, Element{
				ID:     "c",
				Kind:   animation.KindShape,
				Bounds: px(int64(80+col*600), int64(220+row*360), 520, 300),
			})
		}
	}
	if got := classifyGazeLayout(grid, 1, cw, ch); got != gazeLayoutGrid {
		t.Fatalf("six cards = %s, want grid", got)
	}

	list := []Element{
		{ID: "t", Kind: animation.KindTitle, Bounds: px(80, 40, 1600, 80)},
		{ID: "b1", Kind: animation.KindBullet, Bounds: px(120, 200, 1400, 70)},
		{ID: "b2", Kind: animation.KindBullet, Bounds: px(120, 300, 1400, 70)},
		{ID: "b3", Kind: animation.KindBullet, Bounds: px(120, 400, 1400, 70)},
		{ID: "b4", Kind: animation.KindBullet, Bounds: px(120, 500, 1400, 70)},
	}
	if got := classifyGazeLayout(list, 1, cw, ch); got != gazeLayoutList {
		t.Fatalf("bullets = %s, want list", got)
	}

	fig := []Element{
		{ID: "t", Kind: animation.KindTitle, Bounds: px(80, 40, 1600, 80)},
		{ID: "img", Kind: animation.KindChart, Bounds: px(200, 180, 1400, 720)},
	}
	if got := classifyGazeLayout(fig, 1, cw, ch); got != gazeLayoutHero {
		t.Fatalf("big chart = %s, want hero", got)
	}

	bars := []Element{
		{ID: "t", Kind: animation.KindTitle, Bounds: px(80, 40, 1600, 80)},
		{ID: "b1", Kind: animation.KindBullet, Bounds: px(120, 200, 1400, 70)},
		{ID: "b2", Kind: animation.KindBullet, Bounds: px(120, 300, 1400, 70)},
		{ID: "b3", Kind: animation.KindBullet, Bounds: px(120, 400, 1400, 70)},
		{ID: "bar0", Kind: animation.KindBar, Bounds: px(200, 500, 80, 400)},
		{ID: "bar1", Kind: animation.KindBar, Bounds: px(320, 560, 80, 340)},
		{ID: "bar2", Kind: animation.KindBar, Bounds: px(440, 620, 80, 280)},
	}
	if got := classifyGazeLayout(bars, 1, cw, ch); got != gazeLayoutList {
		t.Fatalf("bars must not flip a list into figure, got %s", got)
	}

	rows := []Element{
		{ID: "t", Kind: animation.KindTitle, Bounds: px(80, 40, 1600, 80)},
		{ID: "b1", Kind: animation.KindBullet, Bounds: px(120, 200, 1400, 70)},
		{ID: "b2", Kind: animation.KindBullet, Bounds: px(120, 300, 1400, 70)},
		{ID: "b3", Kind: animation.KindBullet, Bounds: px(120, 400, 1400, 70)},
		{ID: "row0", Kind: animation.KindRow, Bounds: px(200, 500, 1400, 40)},
		{ID: "row1", Kind: animation.KindRow, Bounds: px(200, 540, 1400, 40)},
		{ID: "row2", Kind: animation.KindRow, Bounds: px(200, 580, 1400, 40)},
	}
	if got := classifyGazeLayout(rows, 1, cw, ch); got != gazeLayoutList {
		t.Fatalf("rows must not flip a list into figure, got %s", got)
	}
}

func TestApplyGazeLayoutVocabListDropsLookCrop(t *testing.T) {
	page := Page{
		LookCrop:      true,
		LookCropPadPx: 9,
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "b1", Rect: &Box{X: 1, Y: 1, W: 10, H: 10},
			VeilAlpha: 0.45,
		}},
		Annotations: []Annotation{{Kind: AnnotationCallout}},
	}
	list := []Element{
		{ID: "b1", Kind: animation.KindBullet, Bounds: px(120, 200, 1400, 70)},
		{ID: "b2", Kind: animation.KindBullet, Bounds: px(120, 300, 1400, 70)},
		{ID: "b3", Kind: animation.KindBullet, Bounds: px(120, 400, 1400, 70)},
	}
	applyGazeLayoutVocab(&page, list, 1, 1920, 1080)
	if page.LookCrop {
		t.Fatal("list pages must stay full-page")
	}
	if page.LookCropPadPx != 0 {
		t.Fatalf("pad = %d, want 0", page.LookCropPadPx)
	}
	if page.Focus[0].VeilAlpha <= 0.45 {
		t.Fatalf("list veil = %v, want stronger", page.Focus[0].VeilAlpha)
	}
}

func TestApplyGazeLayoutVocabHeroStaysFullPage(t *testing.T) {
	page := Page{
		LookCrop:      true,
		LookCropPadPx: 9,
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "img", Rect: &Box{X: 200, Y: 180, W: 1400, H: 720},
		}},
		Annotations: []Annotation{{Kind: AnnotationCallout}},
	}
	fig := []Element{
		{ID: "t", Kind: animation.KindTitle, Bounds: px(80, 40, 1600, 80)},
		{ID: "img", Kind: animation.KindChart, Bounds: px(200, 180, 1400, 720)},
	}
	applyGazeLayoutVocab(&page, fig, 1, 1920, 1080)
	if page.LookCrop {
		t.Fatal("hero pages stay full-page until punch-in is an explicit switch")
	}
	if page.LookCropPadPx != 0 {
		t.Fatalf("pad = %d, want 0", page.LookCropPadPx)
	}
	if len(page.Annotations) != 1 {
		t.Fatalf("hero keeps callout, got %+v", page.Annotations)
	}
}

func TestApplyGazeLayoutVocabGridDropsLookCrop(t *testing.T) {
	page := Page{
		LookCrop: true,
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "c", Rect: &Box{X: 80, Y: 220, W: 520, H: 300},
		}},
		Annotations: []Annotation{{Kind: AnnotationCallout}},
	}
	grid := make([]Element, 0, 6)
	for row := 0; row < 2; row++ {
		for col := 0; col < 3; col++ {
			grid = append(grid, Element{
				ID:     "c",
				Kind:   animation.KindShape,
				Bounds: px(int64(80+col*600), int64(220+row*360), 520, 300),
			})
		}
	}
	applyGazeLayoutVocab(&page, grid, 1, 1920, 1080)
	if page.LookCrop {
		t.Fatal("grid cards stay full-page")
	}
	if len(page.Annotations) != 1 {
		t.Fatalf("grid keeps callout, got %+v", page.Annotations)
	}
}
