package render

import (
	"fmt"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

func rowEl(id string, x, y, w, h int64) Element {
	return Element{
		ID: id, Kind: animation.KindRow,
		Bounds: emuBox(x, y, w, h),
	}
}

func TestCompileGazeEntranceFadesRowsAndStaysOnLookChain(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot-b", "spotlight", 0, 240, "withSegment:seg-2"),
			clipOf("c-fade-b", "fadeIn", 0, 400, "withSegment:seg-2"),
		}},
		&animation.Track{ElementID: "table", Kind: animation.KindTable, Clips: []*animation.Clip{
			clipOf("c-table", "fadeIn", 0, 600, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 180, 800, 80)},
			{ID: "table", Kind: animation.KindTable, Bounds: emuBox(72, 202, 1296, 144)},
			rowEl("table-r1", 72, 226, 1296, 24),
			rowEl("table-r2", 72, 250, 1296, 24),
			rowEl("table-r3", 72, 274, 1296, 24),
		},
		SegmentOffsets: map[string]int64{"seg-1": 0, "seg-2": 4000},
		Revision:       rev,
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80}},
			{StartMs: 4000, ElementID: "b", Rect: &Box{X: 10, Y: 180, W: 200, H: 80}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Layer{}
	for _, l := range page.Layers {
		byID[l.ElementID] = l
		if !lookChainLayerOK(l) {
			t.Fatalf("layer %s kicked the look chain: %+v", l.ElementID, l.Clips)
		}
	}
	if _, ok := byID["table"]; ok {
		t.Fatal("whole-table fadeIn must stay skipped")
	}
	if _, ok := byID["a"]; !ok {
		t.Fatal("bullet fadeIn must still compile")
	}
	r1, ok := byID["table-r1"]
	if !ok || len(r1.Clips) != 1 || r1.Clips[0].Effect != "fadeIn" {
		t.Fatalf("row1 = %+v, want fadeIn layer", r1)
	}
	if !isGazeLookPage(page) {
		t.Fatal("row fadeIn must stay on the look chain")
	}
}

func TestRowFadeStartsFollowBeatSheet(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 400, EndMs: 1800, ShapeID: "table-r1", Shot: animation.BeatShotGaze},
		{StartMs: 1800, EndMs: 3600, ShapeID: "table-r2", Shot: animation.BeatShotGaze},
		{StartMs: 3600, EndMs: 8000, ShapeID: "table-r3", Shot: animation.BeatShotGaze},
	}}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)},
			rowEl("table-r1", 72, 226, 1296, 24),
			rowEl("table-r2", 72, 250, 1296, 24),
			rowEl("table-r3", 72, 274, 1296, 24),
		},
		Revision:  rev,
		BeatSheet: sheet,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, l := range page.Layers {
		if l.Kind != animation.KindRow || len(l.Clips) == 0 {
			continue
		}
		got[l.ElementID] = l.Clips[0].StartMs
	}
	want := map[string]int64{"table-r1": 400, "table-r2": 1800, "table-r3": 3600}
	if len(got) != 3 {
		t.Fatalf("row layers = %v, want 3", got)
	}
	for id, ms := range want {
		if got[id] != ms {
			t.Errorf("%s start = %d, want %d", id, got[id], ms)
		}
	}
}

func TestRowFadeEqualSpreadWithoutBeatSheet(t *testing.T) {
	got := barGrowStarts([]Element{
		{ID: "r1"}, {ID: "r2"}, {ID: "r3"}, {ID: "r4"}, {ID: "r5"},
	}, nil, 8000, 400)
	want := []int64{0, 1900, 3800, 5700, 7600}
	if len(got) != 5 {
		t.Fatalf("spread = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spread = %v, want %v", got, want)
		}
	}
}

func TestRowFadeCapsAtSixteen(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	elems := []Element{{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)}}
	for i := 0; i < 20; i++ {
		elems = append(elems, rowEl(fmt.Sprintf("row-%02d", i), 72, int64(200+i*24), 1296, 24))
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
		if l.Kind == animation.KindRow {
			n++
		}
	}
	if n != maxRowFadeLayers {
		t.Fatalf("row layers = %d, want %d", n, maxRowFadeLayers)
	}
}

func TestRowFadeSkippedOnPureGaze(t *testing.T) {
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
			rowEl("table-r1", 72, 226, 1296, 24),
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
		if l.Kind == animation.KindRow {
			t.Fatalf("pure gaze must not synthesize row fade, got %+v", l)
		}
	}
}

func TestRowBeatsAreNotGazeFocusAndPlanValidates(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot-b", "spotlight", 0, 240, "withSegment:seg-2"),
			clipOf("c-fade-b", "fadeIn", 0, 400, "withSegment:seg-2"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 400, EndMs: 1800, ShapeID: "table-r1", Shot: animation.BeatShotGaze},
		{StartMs: 1800, EndMs: 3600, ShapeID: "table-r1", Shot: animation.BeatShotGaze},
		{StartMs: 3600, EndMs: 5200, ShapeID: "a", Shot: animation.BeatShotGaze},
		{StartMs: 5200, EndMs: 8000, ShapeID: "b", Shot: animation.BeatShotGaze},
	}}
	in := PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 180, 800, 80)},
			rowEl("table-r1", 72, 226, 1296, 24),
			rowEl("table-r2", 72, 250, 1296, 24),
		},
		SegmentOffsets: map[string]int64{"seg-1": 0, "seg-2": 3600},
		Revision:       rev,
		BeatSheet:      sheet,
	}
	page, _, err := c.CompilePage(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range page.Focus {
		if f.ElementID == "table-r1" || f.ElementID == "table-r2" {
			t.Fatalf("row must not be a gaze target: %+v", page.Focus)
		}
	}
	nRow := 0
	for _, l := range page.Layers {
		if l.Kind == animation.KindRow {
			nRow++
		}
	}
	if nRow != 2 {
		t.Fatalf("row layers = %d, want 2", nRow)
	}
	plan, err := c.Compile([]PageInput{in})
	if err != nil {
		t.Fatalf("plan with row fade + skipped row beats must validate: %v", err)
	}
	if plan == nil || len(plan.Pages) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestTableOnlyRowBeatsBecomeGazeFocus(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "title", Kind: animation.KindTitle, Clips: []*animation.Clip{
			clipOf("c-title", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 400, EndMs: 2200, ShapeID: "table-r1", Shot: animation.BeatShotGaze, Text: "第一行"},
		{StartMs: 2200, EndMs: 4200, ShapeID: "table-r2", Shot: animation.BeatShotGaze, Text: "第二行"},
		{StartMs: 4200, EndMs: 6400, ShapeID: "table-r3", Shot: animation.BeatShotGaze, Text: "第三行"},
		{StartMs: 6400, EndMs: 9000, ShapeID: "table-r4", Shot: animation.BeatShotGaze, Text: "第四行"},
		{StartMs: 9000, EndMs: 12000, ShapeID: "table-r5", Shot: animation.BeatShotGaze, Text: "第五行"},
	}}
	in := PageInput{
		Index: 0, SlideID: "slide-261", DurationMs: 12000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "title", Kind: animation.KindTitle, Bounds: emuBox(80, 40, 1200, 64), Text: "页眉"},
			rowEl("table-r1", 72, 200, 1296, 48),
			rowEl("table-r2", 72, 248, 1296, 48),
			rowEl("table-r3", 72, 296, 1296, 48),
			rowEl("table-r4", 72, 344, 1296, 48),
			rowEl("table-r5", 72, 392, 1296, 48),
		},
		Revision:  rev,
		BeatSheet: sheet,
	}
	page, _, err := c.CompilePage(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 5 {
		t.Fatalf("table-only looks = %d, want 5 rows: %+v", len(page.Focus), page.Focus)
	}
	for i, f := range page.Focus {
		want := fmt.Sprintf("table-r%d", i+1)
		if f.ElementID != want || f.Rect == nil {
			t.Fatalf("focus[%d] = %+v, want %s with rect", i, f, want)
		}
	}
	if !isGazeLookPage(page) {
		t.Fatal("table-only row looks must stay on the look chain")
	}
	nRow := 0
	for _, l := range page.Layers {
		if l.Kind == animation.KindRow {
			nRow++
		}
	}
	if nRow != 5 {
		t.Fatalf("row fade layers = %d, want 5", nRow)
	}
}

func TestLayerElementIDsIncludesSynthesizedRows(t *testing.T) {
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	elems := []Element{
		{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 80, 800, 80)},
		rowEl("table-r1", 72, 226, 1296, 24),
		rowEl("table-r2", 72, 250, 1296, 24),
	}
	got := LayerElementIDs(rev, elems, design.Canvas{Width: 1920, Height: 1080}, emuHeight)
	if _, ok := got["a"]; !ok {
		t.Fatalf("missing bullet: %v", got)
	}
	if _, ok := got["table-r1"]; !ok {
		t.Fatalf("missing row: %v", got)
	}
	if _, ok := got["table-r2"]; !ok {
		t.Fatalf("missing row: %v", got)
	}
}
