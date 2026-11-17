package render

import (
	"fmt"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

func barEl(id, axis, origin string, x, y, w, h int64) Element {
	return Element{
		ID: id, Kind: animation.KindBar, GrowAxis: axis, GrowOrigin: origin,
		Bounds: emuBox(x, y, w, h),
	}
}

func TestCompileGazeEntranceGrowsBarsAndLeavesLookChain(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "chart", Kind: animation.KindChart, Clips: []*animation.Clip{
			clipOf("c-grow", "grow", 0, 800, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "chart", Kind: animation.KindChart, Bounds: emuBox(200, 400, 600, 320)},
			barEl("chart-b0", "y", OriginBottom, 220, 520, 60, 180),
			barEl("chart-b1", "y", OriginBottom, 300, 480, 60, 220),
			barEl("chart-b2", "y", OriginTop, 380, 400, 60, 160),
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Layer{}
	for _, l := range page.Layers {
		byID[l.ElementID] = l
	}
	if _, ok := byID["chart"]; ok {
		t.Fatal("whole-chart grow must stay skipped")
	}
	if _, ok := byID["a"]; !ok {
		t.Fatal("bullet fadeIn must still compile")
	}
	b0, ok := byID["chart-b0"]
	if !ok || len(b0.Clips) != 1 || b0.Clips[0].Effect != "grow" {
		t.Fatalf("bar0 = %+v, want grow layer", b0)
	}
	if b0.Origin != OriginBottom {
		t.Fatalf("bar0 origin = %q, want bottom", b0.Origin)
	}
	b2, ok := byID["chart-b2"]
	if !ok {
		t.Fatal("negative bar missing")
	}
	if b2.Origin != OriginTop {
		t.Fatalf("negative bar origin = %q, want top", b2.Origin)
	}
	if isGazeLookPage(page) {
		t.Fatal("grow layers must kick the page off the look chain")
	}
	if !pageNeedsCompositor(page) {
		t.Fatal("bar grow page must go Chrome")
	}
}

func TestCompileTiltClipLeavesLookChain(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "beam", Kind: animation.KindShape, Clips: []*animation.Clip{
			{ID: "c-tilt", Type: "tilt", StartMs: 400, DurationMs: 800, Easing: "easeOut", Trigger: "onEnter",
				Params: animation.Params{Angle: 10}},
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "beam", Kind: animation.KindShape, Bounds: emuBox(200, 500, 800, 40)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80}},
			{StartMs: 4000, ElementID: "beam", Rect: &Box{X: 200, Y: 500, W: 800, H: 40}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var beam Layer
	found := false
	for _, l := range page.Layers {
		if l.ElementID == "beam" {
			beam, found = l, true
			break
		}
	}
	if !found || len(beam.Clips) != 1 || beam.Clips[0].Effect != "tilt" {
		t.Fatalf("beam = %+v, want tilt layer", beam)
	}
	got := false
	for _, p := range beam.Clips[0].Props {
		if p.Name == animation.PropRotate && p.To == 10 {
			got = true
		}
	}
	if !got {
		t.Fatalf("tilt props = %+v, want rotate 0→10", beam.Clips[0].Props)
	}
	if isGazeLookPage(page) {
		t.Fatal("tilt must kick the page off the look chain")
	}
	if !pageNeedsCompositor(page) {
		t.Fatal("tilt page must go Chrome")
	}
}

func TestBarGrowStartsFollowBeatSheet(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 400, EndMs: 1800, ShapeID: "chart-b0", Shot: animation.BeatShotGaze},
		{StartMs: 1800, EndMs: 3600, ShapeID: "chart-b1", Shot: animation.BeatShotGaze},
		{StartMs: 3600, EndMs: 8000, ShapeID: "chart-b2", Shot: animation.BeatShotGaze},
	}}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)},
			barEl("chart-b0", "y", OriginBottom, 220, 520, 60, 180),
			barEl("chart-b1", "y", OriginBottom, 300, 480, 60, 220),
			barEl("chart-b2", "y", OriginBottom, 380, 500, 60, 200),
		},
		Revision:  rev,
		BeatSheet: sheet,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, l := range page.Layers {
		if l.Kind != animation.KindBar || len(l.Clips) == 0 {
			continue
		}
		got[l.ElementID] = l.Clips[0].StartMs
	}
	want := map[string]int64{"chart-b0": 400, "chart-b1": 1800, "chart-b2": 3600}
	if len(got) != 3 {
		t.Fatalf("bar layers = %v, want 3", got)
	}
	for id, ms := range want {
		if got[id] != ms {
			t.Errorf("%s start = %d, want %d", id, got[id], ms)
		}
	}
}

func TestBarGrowEqualSpreadWithoutBeatSheet(t *testing.T) {
	got := barGrowStarts([]Element{
		{ID: "b0"}, {ID: "b1"}, {ID: "b2"},
	}, nil, 8000, 900)
	want := []int64{0, 3550, 7100}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("spread = %v, want %v", got, want)
	}
}

func TestBarGrowCapsAtSixteen(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	elems := []Element{{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)}}
	for i := 0; i < 20; i++ {
		elems = append(elems, barEl(fmt.Sprintf("bar-%02d", i), "y", OriginBottom, int64(100+i*40), 400, 30, 200))
	}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 12000, BaseImage: "pages/p0.png",
		Elements: elems, Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range page.Layers {
		if l.Kind == animation.KindBar {
			n++
		}
	}
	if n != maxBarGrowLayers {
		t.Fatalf("bar layers = %d, want %d", n, maxBarGrowLayers)
	}
}

func TestBarGrowSkippedOnPureGaze(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			barEl("chart-b0", "y", OriginBottom, 220, 520, 60, 180),
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range page.Layers {
		if l.Kind == animation.KindBar {
			t.Fatalf("pure gaze must not synthesize grow, got %+v", l)
		}
	}
}

func TestLayerElementIDsIncludesSynthesizedBars(t *testing.T) {
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	elems := []Element{
		{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)},
		barEl("chart-b0", "y", OriginBottom, 220, 520, 60, 180),
		barEl("chart-b1", "y", OriginBottom, 300, 480, 60, 220),
	}
	got := LayerElementIDs(rev, elems, design.Canvas{Width: 1920, Height: 1080}, emuHeight)
	if _, ok := got["a"]; !ok {
		t.Fatalf("missing bullet: %v", got)
	}
	if _, ok := got["chart-b0"]; !ok {
		t.Fatalf("missing bar: %v", got)
	}
	if _, ok := got["chart-b1"]; !ok {
		t.Fatalf("missing bar: %v", got)
	}
}
