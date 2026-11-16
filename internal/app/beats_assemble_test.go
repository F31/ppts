package app

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

func TestT9PreviewExportUseDisplayText(t *testing.T) {
	page := &project.Page{
		Index: 0, SlideID: "s1",
		Shapes: []*project.Shape{
			{ID: "c1", Kind: "text", Text: "智慧城市", Bounds: &project.Box{X: 406400, Y: 1231900, Width: 2724150, Height: 1473200}},
			{ID: "c2", Kind: "text", Text: "智能办公", Bounds: &project.Box{X: 3213100, Y: 1231900, Width: 2724150, Height: 1473200}},
		},
	}
	slide := media.SlideCue{
		SlideID: "s1", StartUS: 0, EndUS: 10_000_000,
		Segments: []media.SegmentCue{
			{SegmentID: "seg-1", StartUS: 0, EndUS: 10_000_000, AlignmentMethod: tts.AlignEstimate},
		},
	}
	subs := []media.SubtitleCue{{
		SlideID: "s1", SegmentID: "seg-1", StartUS: 0, EndUS: 10_000_000,
		Text: "首先看智慧城市。接着看智能办公。",
	}}
	elements := PageElements(page)
	bindElems := PageBindElements(page, 12192000, 6858000)
	ids := compileIDsOf(elements)
	preview := PageBeatSheet(bindElems, ids, slide, subs, 10000)
	export := PageBeatSheet(bindElems, ids, slide, subs, 10000)
	if !preview.Sheet.Valid() || !export.Sheet.Valid() {
		t.Fatalf("preview reason=%s export reason=%s", preview.Reason, export.Reason)
	}
	if preview.Reason != export.Reason || len(preview.Sheet.Beats) != len(export.Sheet.Beats) {
		t.Fatalf("preview/export BeatSheet diverged: %+v vs %+v", preview, export)
	}

	rev := &animation.Revision{
		PresetID:   "teaching-clear",
		Intensity:  1,
		MotionMode: animation.MotionModeGaze,
		Tracks: []*animation.Track{
			{ElementID: "c1", Kind: animation.KindTitle, Clips: []*animation.Clip{
				{ID: "a", Type: "spotlight", StartMs: 0, DurationMs: 240, Trigger: "withSegment:seg-1"},
			}},
			{ElementID: "c2", Kind: animation.KindTitle, Clips: []*animation.Clip{
				{ID: "b", Type: "spotlight", StartMs: 140, DurationMs: 240, Trigger: "withSegment:seg-1"},
			}},
		},
	}
	c, err := render.NewCompiler(render.Options{
		Canvas: design.Canvas{Width: 1920, Height: 1080},
		FPS:    30, Background: "#FFFFFF",
		SlideWidthEMU: 12192000, SlideHeightEMU: 6858000,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := preview.Sheet
	es := export.Sheet
	pPage, _, err := c.CompilePage(render.PageInput{
		Index: 0, SlideID: "s1", DurationMs: 10000, BaseImage: "pages/p0.png",
		Elements: elements, Revision: rev, SegmentOffsets: map[string]int64{"seg-1": 0},
		Focus: nil, BeatSheet: &ps, GazeDenyIDs: preview.DenyIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	ePage, _, err := c.CompilePage(render.PageInput{
		Index: 0, SlideID: "s1", DurationMs: 10000, BaseImage: "pages/p0.png",
		Elements: elements, Revision: rev, SegmentOffsets: map[string]int64{"seg-1": 0},
		Focus: nil, BeatSheet: &es, GazeDenyIDs: export.DenyIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pPage.Focus) != len(ePage.Focus) || len(pPage.Focus) < 2 {
		t.Fatalf("focus preview=%d export=%d", len(pPage.Focus), len(ePage.Focus))
	}
	for i := range pPage.Focus {
		if pPage.Focus[i].StartMs != ePage.Focus[i].StartMs || pPage.Focus[i].ElementID != ePage.Focus[i].ElementID {
			t.Fatalf("focus[%d] preview=%+v export=%+v", i, pPage.Focus[i], ePage.Focus[i])
		}
		if pPage.Focus[i].Rect == nil || ePage.Focus[i].Rect == nil || *pPage.Focus[i].Rect != *ePage.Focus[i].Rect {
			t.Fatalf("rect[%d] preview=%v export=%v", i, pPage.Focus[i].Rect, ePage.Focus[i].Rect)
		}
	}

	spokenMismatch := []media.SubtitleCue{{
		SlideID: "s1", SegmentID: "seg-1", StartUS: 0, EndUS: 10_000_000,
		Text: "首先看智慧城市。接着看智能办公。",
	}}
	got := PageBeatSheet(bindElems, ids, slide, spokenMismatch, 10000)
	if got.Sheet.Beats[0].ShapeID != preview.Sheet.Beats[0].ShapeID {
		t.Fatal("DisplayText 路径必须稳定，不读 SpokenText")
	}
}

func TestPageBeatSheetIgnoresOtherSlides(t *testing.T) {
	page := &project.Page{
		Index: 0, SlideID: "s1",
		Shapes: []*project.Shape{
			{ID: "c1", Kind: "text", Text: "智慧城市", Bounds: &project.Box{X: 406400, Y: 1231900, Width: 2724150, Height: 1473200}},
			{ID: "c2", Kind: "text", Text: "智能办公", Bounds: &project.Box{X: 3213100, Y: 1231900, Width: 2724150, Height: 1473200}},
		},
	}
	slide := media.SlideCue{SlideID: "s1", StartUS: 0, EndUS: 8000000}
	out := PageBeatSheet(PageBindElements(page, 12192000, 6858000), compileIDsOf(PageElements(page)), slide, []media.SubtitleCue{
		{SlideID: "s2", SegmentID: "x", Text: "首先看智慧城市。接着看智能办公。", StartUS: 0, EndUS: 8000000},
	}, 8000)
	if out.Reason != "no_speech" || out.Sheet.Valid() {
		t.Fatalf("other-slide subtitles must not produce a sheet: %+v", out)
	}
}
