package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func TestCompileSpreadsClipsPackedOnOneSegment(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 30_000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
			{ID: "c", Kind: animation.KindBullet, Bounds: emuBox(96, 400, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision: revisionOf("teaching-clear", 1,
			&animation.Track{ElementID: "a", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")}},
			&animation.Track{ElementID: "b", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c2", "fadeIn", 140, 400, "withSegment:seg-1")}},
			&animation.Track{ElementID: "c", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c3", "fadeIn", 280, 400, "withSegment:seg-1")}},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	starts := map[string]int64{}
	for _, ly := range page.Layers {
		if len(ly.Clips) > 0 {
			starts[ly.ElementID] = ly.Clips[0].StartMs
		}
	}
	if starts["a"] != 0 || starts["b"] != 10_000 || starts["c"] != 20_000 {
		t.Fatalf("starts = %v, want 0/10000/20000", starts)
	}
}

func TestCompileKeepsHandTimedClips(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 30_000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision: revisionOf("teaching-clear", 1,
			&animation.Track{ElementID: "a", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")}},
			&animation.Track{ElementID: "b", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c2", "fadeIn", 12_000, 400, "withSegment:seg-1")}},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	starts := map[string]int64{}
	for _, ly := range page.Layers {
		if len(ly.Clips) > 0 {
			starts[ly.ElementID] = ly.Clips[0].StartMs
		}
	}
	if starts["a"] != 0 || starts["b"] != 12_000 {
		t.Fatalf("hand-timed starts changed: %v", starts)
	}
}
