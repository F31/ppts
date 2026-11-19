package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func startsForText(text string) []int64 {
	out := make([]int64, len([]rune(text)))
	for i := range out {
		out[i] = int64(1000 + i*10)
	}
	return out
}

func TestContainerOverviewStartsAtHeader(t *testing.T) {
	c := newTestCompiler(t)
	text := "intro 时间 节点 要点 table"
	cues := map[string]string{"seg-1": text}
	starts := map[string][]int64{"seg-1": startsForText(text)}
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 5000, EndMs: 8000, ShapeID: "table-r1", Shot: animation.BeatShotGaze, Text: "第一行"},
		{StartMs: 8000, EndMs: 11000, ShapeID: "table-r2", Shot: animation.BeatShotGaze, Text: "第二行"},
	}}
	rev := revisionOf("tech-dynamic", 1.15)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-table", DurationMs: 12000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "table", Kind: animation.KindTable, Bounds: emuBox(72, 202, 1296, 200), Text: "时间\t节点\t要点\n2026.1\tQwen3-Max"},
			rowEl("table-r1", 72, 228, 1296, 48),
			rowEl("table-r2", 72, 276, 1296, 48),
		},
		SegmentTexts:      cues,
		SegmentCharStarts: starts,
		Revision:          rev,
		BeatSheet:         sheet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 3 {
		t.Fatalf("focus = %+v, want table overview + 2 rows", page.Focus)
	}
	if page.Focus[0].ElementID != "table" {
		t.Fatalf("first focus = %+v, want table overview", page.Focus[0])
	}
	if page.Focus[0].StartMs <= 0 || page.Focus[0].EndMs != 5000 {
		t.Fatalf("overview = %+v, want start before first row and end 5000", page.Focus[0])
	}
	if page.Focus[0].StartMs >= page.Focus[1].StartMs {
		t.Fatalf("overview start %d must precede first row %d", page.Focus[0].StartMs, page.Focus[1].StartMs)
	}
}

func TestPointersSkipTableAndRows(t *testing.T) {
	c := newTestCompiler(t)
	spans := []FocusSpan{
		{StartMs: 0, EndMs: 1000, ElementID: "table", Rect: boxPtr(10, 20, 100, 40)},
		{StartMs: 1000, EndMs: 2000, ElementID: "row", Rect: boxPtr(10, 60, 100, 20)},
		{StartMs: 2000, EndMs: 3000, ElementID: "title", Rect: boxPtr(10, 90, 100, 40)},
	}
	els := []Element{
		{ID: "table", Kind: animation.KindTable, Bounds: emuBox(10, 20, 100, 40), Text: "时间 节点"},
		{ID: "row", Kind: animation.KindRow, Bounds: emuBox(10, 60, 100, 20), Text: "2026.1"},
		{ID: "title", Kind: animation.KindTitle, Bounds: emuBox(10, 90, 100, 40), Text: "标题"},
	}
	got := pointersFromFocus(spans, els, nil, c.scale, 1920, 1080)
	if len(got) != 1 {
		t.Fatalf("pointers = %+v, want only title pointer", got)
	}
	if got[0].ElementID != "title" {
		t.Fatalf("pointer = %+v, want title", got[0])
	}
}

func boxPtr(x, y, w, h int) *Box { return &Box{X: x, Y: y, W: w, H: h} }
