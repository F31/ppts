package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func TestFlowCompilesToOverlay(t *testing.T) {
	c := newTestCompiler(t)
	els := []Element{
		{ID: "a", Kind: animation.KindTitle, Bounds: emuBox(100, 100, 300, 80)},
		{ID: "b", Kind: animation.KindTitle, Bounds: emuBox(800, 500, 300, 80)},
	}
	rev := revisionOf("teaching-clear", 1)
	rev.MotionMode = animation.MotionModeGaze
	bs := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 0, SegmentID: "s1", ShapeID: "a", Shot: animation.BeatShotGaze},
		{StartMs: 3000, SegmentID: "s2", ShapeID: "b", Shot: animation.BeatShotGaze,
			Intent: &animation.BeatIntent{Flow: &animation.BeatFlow{
				From: "a", To: "b", Carrier: OverlayDot, Route: "orthogonal",
				StartMs: 3500, DurationMs: 800,
			}}},
	}}
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 6000, BaseImage: "p.png",
		Elements: els, Revision: rev, BeatSheet: bs, GazeDenyIDs: map[string]struct{}{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(page.Overlays) != 1 {
		t.Fatalf("overlays = %d, want 1", len(page.Overlays))
	}
	o := page.Overlays[0]
	if o.Kind != OverlayDot || o.StartMs != 3500 || o.DurationMs != 800 || len(o.Points) != 3 {
		t.Fatalf("overlay = %+v", o)
	}
}
