package render

import (
	"fmt"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

func TestFocusFromSegmentTracksNeedsTwoLayers(t *testing.T) {
	rev := revisionOf("teaching-clear", 1, &animation.Track{
		ElementID: "a", Kind: animation.KindBullet,
		Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")},
	})
	got := FocusFromSegmentTracks(rev, map[string]int64{"seg-1": 0}, 5000, map[string]struct{}{"a": {}})
	if got != nil {
		t.Fatalf("one layer must not dim anyone, got %+v", got)
	}
}

func TestFocusFromSegmentTracksOrdersBySegmentOffset(t *testing.T) {
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "fadeIn", 0, 400, "withSegment:seg-2")}},
		&animation.Track{ElementID: "a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")}},
	)
	got := FocusFromSegmentTracks(rev, map[string]int64{"seg-1": 200, "seg-2": 1800}, 6000,
		map[string]struct{}{"a": {}, "b": {}})
	if len(got) != 2 {
		t.Fatalf("spans = %d, want 2", len(got))
	}
	if got[0].ElementID != "a" || got[0].StartMs != 200 {
		t.Fatalf("first = %+v, want a@200", got[0])
	}
	if got[1].ElementID != "b" || got[1].StartMs != 1800 {
		t.Fatalf("second = %+v, want b@1800", got[1])
	}
}

func TestCompileTeachingClearAddsFocusFromSegments(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 6000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0, "seg-2": 2000},
		Revision: revisionOf("teaching-clear", 1,
			&animation.Track{ElementID: "a", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")}},
			&animation.Track{ElementID: "b", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c2", "fadeIn", 0, 400, "withSegment:seg-2")}},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 2 {
		t.Fatalf("focus = %d, want 2: %+v", len(page.Focus), page.Focus)
	}
	if page.Focus[0].ElementID != "a" || page.Focus[1].ElementID != "b" {
		t.Fatalf("focus ids = %s,%s", page.Focus[0].ElementID, page.Focus[1].ElementID)
	}
}

func TestCompileGazeFillsRectOnElementIDOnlyFocus(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "spotlight", 0, 240, "withSegment:seg-1")}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "spotlight", 140, 240, "withSegment:seg-1")}},
	)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 80000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a"},
			{StartMs: 2000, ElementID: "b"},
		},
		Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 0 {
		t.Fatalf("gaze spotlight must not become layers, got %d", len(page.Layers))
	}
	if len(page.Focus) != 2 {
		t.Fatalf("focus = %d, want 2: %+v", len(page.Focus), page.Focus)
	}
	for i, f := range page.Focus {
		if f.Rect == nil || f.Rect.W <= 0 || f.Rect.H <= 0 {
			t.Fatalf("focus[%d] missing rect: %+v", i, f)
		}
	}
	if page.Focus[0].StartMs != 0 || page.Focus[1].StartMs != 2000 {
		t.Fatalf("assembler starts must be kept, got %d,%d", page.Focus[0].StartMs, page.Focus[1].StartMs)
	}
}

func TestCompileGazeSpotlightAndFadeInSameTrack(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c2", "spotlight", 140, 240, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 1 {
		t.Fatalf("layers = %d, want 1 (only fadeIn)", len(page.Layers))
	}
	if page.Layers[0].ElementID != "a" {
		t.Fatalf("layer = %s, want a", page.Layers[0].ElementID)
	}
	if len(page.Layers[0].Clips) != 1 || page.Layers[0].Clips[0].Effect != "fadeIn" {
		t.Fatalf("clips = %+v, want one fadeIn", page.Layers[0].Clips)
	}
	if len(page.Focus) < 2 {
		t.Fatalf("focus = %+v, want two rects", page.Focus)
	}
	for i, f := range page.Focus {
		if f.Rect == nil {
			t.Fatalf("focus[%d] missing rect", i)
		}
	}
}

func TestCompileGazeSpreadsFiveSpotlightsOver80s(t *testing.T) {
	c := newTestCompiler(t)
	tracks := make([]*animation.Track, 5)
	ids := []string{"a", "b", "c", "d", "e"}
	for i, id := range ids {
		tracks[i] = &animation.Track{ElementID: id, Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c"+id, "spotlight", int64(i)*140, 240, "withSegment:seg-1")}}
	}
	rev := revisionOf("teaching-clear", 1, tracks...)
	rev.MotionMode = animation.MotionModeGaze
	elems := make([]Element, 5)
	for i, id := range ids {
		elems[i] = Element{ID: id, Kind: animation.KindBullet, Bounds: emuBox(96, int64(200+i*80), 800, 72)}
	}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 80000, BaseImage: "pages/p0.png",
		Elements: elems, SegmentOffsets: map[string]int64{"seg-1": 0}, Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 0 {
		t.Fatalf("gaze must not emit layers, got %d", len(page.Layers))
	}
	if len(page.Focus) != 5 {
		t.Fatalf("focus = %d, want 5: %+v", len(page.Focus), page.Focus)
	}
	var callouts, pointers int
	for i, a := range page.Annotations {
		if a.Rect == nil {
			t.Fatalf("annotation[%d] missing rect: %+v", i, a)
		}
		switch a.Kind {
		case AnnotationCallout:
			callouts++
		case AnnotationPointer:
			pointers++
		case AnnotationCaption:
			t.Fatal("compile must not emit caption for MP4")
		default:
			t.Fatalf("annotation[%d] kind = %q", i, a.Kind)
		}
	}
	if callouts != 5 {
		t.Fatalf("callouts = %d, want 5", callouts)
	}
	if pointers != 5 {
		t.Fatalf("pointers = %d, want 5", pointers)
	}
	want := []int64{0, 16000, 32000, 48000, 64000}
	for i, f := range page.Focus {
		if f.StartMs != want[i] {
			t.Fatalf("focus[%d].StartMs = %d, want %d", i, f.StartMs, want[i])
		}
		if f.Rect == nil || f.Rect.W <= 0 {
			t.Fatalf("focus[%d] missing rect", i)
		}
		if f.VeilAlpha <= 0 {
			t.Fatalf("focus[%d] veilAlpha not baked", i)
		}
		if i+1 < len(want) && page.Annotations[i].EndMs != want[i+1] {
			t.Fatalf("callout[%d].EndMs = %d, want next look %d", i, page.Annotations[i].EndMs, want[i+1])
		}
	}
	if page.Annotations[4].EndMs != 0 {
		t.Fatalf("last callout EndMs = %d, want 0 (hold to page end)", page.Annotations[4].EndMs)
	}
}

func TestAnnotationsFromFocusCloseWhenNextLookStarts(t *testing.T) {
	rect := &Box{X: 10, Y: 10, W: 100, H: 40}
	spans := []FocusSpan{
		{StartMs: 0, ElementID: "a", Rect: rect, FadeMs: 240},
		{StartMs: 16000, ElementID: "b", Rect: rect, FadeMs: 240},
		{StartMs: 32000, ElementID: "c", Rect: rect, FadeMs: 240},
	}
	got := annotationsFromFocus(spans, "rect")
	if len(got) != 3 {
		t.Fatalf("callouts = %d, want 3", len(got))
	}
	if got[0].EndMs != 16000 || got[1].EndMs != 32000 {
		t.Fatalf("endMs = %d %d, want 16000 32000", got[0].EndMs, got[1].EndMs)
	}
	if got[2].EndMs != 0 {
		t.Fatalf("last endMs = %d, want 0", got[2].EndMs)
	}
	kept := annotationsFromFocus([]FocusSpan{
		{StartMs: 0, EndMs: 900, ElementID: "a", Rect: rect},
		{StartMs: 2000, ElementID: "b", Rect: rect},
	}, "rect")
	if kept[0].EndMs != 900 {
		t.Fatalf("explicit EndMs = %d, want 900", kept[0].EndMs)
	}
}

func TestGazeRectExpandsToContainingCard(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "title-a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "spotlight", 0, 240, "withSegment:seg-1")}},
		&animation.Track{ElementID: "title-b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "spotlight", 140, 240, "withSegment:seg-1")}},
	)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 80000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "title-a", Kind: animation.KindBullet, Bounds: emuBox(120, 220, 360, 48)},
			{ID: "title-b", Kind: animation.KindBullet, Bounds: emuBox(620, 220, 360, 48)},
			{ID: "card-a", Kind: animation.KindShape, Bounds: emuBox(96, 200, 420, 280)},
			{ID: "card-b", Kind: animation.KindShape, Bounds: emuBox(596, 200, 420, 280)},
			{ID: "page-bg", Kind: animation.KindShape, Bounds: emuBox(0, 0, 1920, 1080)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 2 {
		t.Fatalf("focus = %d, want 2: %+v", len(page.Focus), page.Focus)
	}
	a, b := page.Focus[0].Rect, page.Focus[1].Rect
	if a == nil || b == nil {
		t.Fatal("missing rect")
	}
	if a.W < 400 || a.H < 260 {
		t.Fatalf("card-a rect = %+v, want padded card ~420x280", a)
	}
	if b.W < 400 || b.H < 260 {
		t.Fatalf("card-b rect = %+v, want padded card ~420x280", b)
	}
	if a.X > 96 || b.X < 500 {
		t.Fatalf("rects should sit on distinct cards, got a=%+v b=%+v", a, b)
	}
}

func TestGazeMergesLooksOnSameCard(t *testing.T) {
	c := newTestCompiler(t)
	// 标题先、正文后：title-a 与 body-a 不相邻，相邻去重接不住。
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "title-a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "spotlight", 0, 240, "withSegment:seg-1")}},
		&animation.Track{ElementID: "title-b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "spotlight", 140, 240, "withSegment:seg-1")}},
		&animation.Track{ElementID: "body-a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c3", "spotlight", 280, 240, "withSegment:seg-1")}},
	)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 60000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "title-a", Kind: animation.KindBullet, Bounds: emuBox(120, 220, 360, 48)},
			{ID: "body-a", Kind: animation.KindBullet, Bounds: emuBox(120, 280, 360, 80)},
			{ID: "title-b", Kind: animation.KindBullet, Bounds: emuBox(620, 220, 360, 48)},
			{ID: "card-a", Kind: animation.KindShape, Bounds: emuBox(96, 200, 420, 280)},
			{ID: "card-b", Kind: animation.KindShape, Bounds: emuBox(596, 200, 420, 280)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 2 {
		t.Fatalf("same-card title+body must merge, focus = %d: %+v", len(page.Focus), page.Focus)
	}
	if page.Focus[0].StartMs != 0 {
		t.Fatalf("first look start = %d, want 0", page.Focus[0].StartMs)
	}
	if page.Focus[1].StartMs != 30000 {
		t.Fatalf("second look start = %d, want duration/2=30000", page.Focus[1].StartMs)
	}
	if page.Annotations[0].EndMs != page.Focus[1].StartMs {
		t.Fatalf("callout end = %d, want next look %d", page.Annotations[0].EndMs, page.Focus[1].StartMs)
	}
}

func TestMergeGazeLooksUniqueByRect(t *testing.T) {
	cards := []Box{
		{X: 64, Y: 194, W: 429, H: 232},
		{X: 506, Y: 194, W: 429, H: 232},
		{X: 947, Y: 194, W: 429, H: 232},
		{X: 64, Y: 438, W: 429, H: 232},
		{X: 506, Y: 438, W: 429, H: 232},
		{X: 947, Y: 438, W: 429, H: 232},
	}
	var spans []FocusSpan
	for i := range cards {
		r := cards[i]
		spans = append(spans, FocusSpan{
			StartMs: int64(i) * 1000, ElementID: fmt.Sprintf("t%d", i), Rect: &r,
		})
	}
	for i := range cards {
		r := cards[i]
		spans = append(spans, FocusSpan{
			StartMs: int64(6+i) * 1000, ElementID: fmt.Sprintf("b%d", i), Rect: &r,
		})
	}
	got := mergeGazeLooks(spans, 60000, false)
	if len(got) != 6 {
		t.Fatalf("got %d looks, want 6: %+v", len(got), got)
	}
	for i, s := range got {
		want := int64(i) * 10000
		if s.StartMs != want {
			t.Fatalf("look %d start=%d, want %d", i, s.StartMs, want)
		}
		if s.Rect == nil || *s.Rect != cards[i] {
			t.Fatalf("look %d rect=%+v, want %+v", i, s.Rect, cards[i])
		}
		if s.EndMs != 0 {
			t.Fatalf("look %d EndMs=%d, want 0 after re-spread", i, s.EndMs)
		}
	}
	kept := mergeGazeLooks(got, 60000, false)
	if len(kept) != 6 || kept[3].StartMs != 30000 {
		t.Fatalf("no-collapse must keep times, got %+v", kept)
	}
}

func TestMergeGazeLooksKeepsPhraseStillsSameElement(t *testing.T) {
	r := Box{X: 64, Y: 194, W: 429, H: 232}
	copyR := r
	spans := []FocusSpan{
		{StartMs: 0, EndMs: 8000, ElementID: "c1", Rect: &r},
		{StartMs: 8000, EndMs: 16000, ElementID: "c1", Rect: &copyR},
	}
	got := mergeGazeLooks(spans, 20000, true)
	if len(got) != 2 {
		t.Fatalf("same-element phrase stills must keep both, got %d: %+v", len(got), got)
	}
	if got[0].StartMs != 0 || got[1].StartMs != 8000 {
		t.Fatalf("times = %d %d, want 0 8000", got[0].StartMs, got[1].StartMs)
	}
}

func TestFocusFromBeatSheetHoldEmitsPhraseLooks(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "spotlight", 0, 240, "withSegment:seg-1")}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "spotlight", 140, 240, "withSegment:seg-1")}},
	)
	rev.MotionMode = animation.MotionModeGaze
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 0, EndMs: 4000, ShapeID: "a", Shot: animation.BeatShotGaze, Text: "先讲这张卡的上半句"},
		{StartMs: 4000, EndMs: 8000, Shot: animation.BeatShotHold, Text: "同一张卡的下半句"},
		{StartMs: 8000, EndMs: 16000, ShapeID: "b", Shot: animation.BeatShotGaze, Text: "切到第二张"},
	}}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 16000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80), Text: "先讲这张卡的上半句同一张卡的下半句"},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 320, 800, 80), Text: "切到第二张"},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev, BeatSheet: sheet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 3 {
		t.Fatalf("hold phrase still missing, looks=%d: %+v", len(page.Focus), page.Focus)
	}
	if page.Focus[0].ElementID != "a" || page.Focus[1].ElementID != "a" || page.Focus[2].ElementID != "b" {
		t.Fatalf("look ids = %s %s %s, want a a b", page.Focus[0].ElementID, page.Focus[1].ElementID, page.Focus[2].ElementID)
	}
	if page.Focus[1].StartMs != 4000 {
		t.Fatalf("phrase look start = %d, want 4000", page.Focus[1].StartMs)
	}
	var pointers int
	for _, a := range page.Annotations {
		if a.Kind == AnnotationPointer {
			pointers++
		}
	}
	if pointers != 3 {
		t.Fatalf("pointers = %d, want 3", pointers)
	}
}

func TestFocusFromBeatSheetDenseDropsPhraseExtras(t *testing.T) {
	c := newTestCompiler(t)
	tracks := make([]*animation.Track, 6)
	elems := make([]Element, 6)
	beats := make([]animation.Beat, 0, 12)
	t0 := int64(0)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("c%d", i+1)
		tracks[i] = &animation.Track{ElementID: id, Kind: animation.KindShape,
			Clips: []*animation.Clip{clipOf("s"+id, "spotlight", 0, 240, "withSegment:seg-1")}}
		col, row := i%3, i/3
		x, y := int64(80+col*620), int64(220+row*380)
		elems[i] = Element{ID: id, Kind: animation.KindShape, Bounds: emuBox(x, y, 560, 320), Text: id}
		beats = append(beats, animation.Beat{StartMs: t0, EndMs: t0 + 4000, ShapeID: id, Shot: animation.BeatShotGaze, Text: id})
		beats = append(beats, animation.Beat{StartMs: t0 + 4000, EndMs: t0 + 8000, Shot: animation.BeatShotHold, Text: id + "下半句"})
		t0 += 8000
	}
	rev := revisionOf("teaching-clear", 1, tracks...)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: t0, BaseImage: "pages/p0.png",
		Elements: elems, SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision: rev, BeatSheet: &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: beats},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 6 {
		t.Fatalf("dense page must keep 6 unique looks, got %d: %+v", len(page.Focus), page.Focus)
	}
	seen := map[string]int{}
	for _, f := range page.Focus {
		seen[f.ElementID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("element %s appears %d times, want 1", id, n)
		}
	}
}

func TestT6KeepTimesUniqueByRect(t *testing.T) {
	cardA := Box{X: 64, Y: 194, W: 429, H: 232}
	cardB := Box{X: 506, Y: 194, W: 429, H: 232}
	spans := []FocusSpan{
		{StartMs: 12000, EndMs: 18000, ElementID: "title-a", Rect: &cardA},
		{StartMs: 18000, EndMs: 40000, ElementID: "body-a", Rect: &cardA},
		{StartMs: 40000, ElementID: "title-b", Rect: &cardB},
	}
	got := mergeGazeLooks(spans, 80000, true)
	if len(got) != 2 {
		t.Fatalf("got %d looks, want 2: %+v", len(got), got)
	}
	if got[0].StartMs != 18000 || got[0].EndMs != 40000 {
		t.Fatalf("collapsed card StartMs=%d EndMs=%d, want 18000/40000", got[0].StartMs, got[0].EndMs)
	}
	if got[0].ElementID != "body-a" {
		t.Fatalf("keep later body, got %s", got[0].ElementID)
	}
	if got[1].StartMs != 40000 || got[1].ElementID != "title-b" {
		t.Fatalf("second = %+v, want title-b@40000", got[1])
	}
	if got[1].StartMs <= got[0].StartMs {
		t.Fatal("array must be time order, not (Y,X)")
	}
	anns := annotationsFromFocus(got, "rect")
	if len(anns) != 2 {
		t.Fatalf("callouts = %d, want 2", len(anns))
	}
	if anns[0].EndMs != 40000 {
		t.Fatalf("first callout EndMs=%d, want 40000", anns[0].EndMs)
	}
}

func TestGazeFallbackKeepsSiblingsInSharedPanel(t *testing.T) {
	c := newTestCompiler(t)
	panel := Element{ID: "panel", Kind: animation.KindShape, Bounds: emuBox(80, 180, 760, 620)}
	rows := []Element{
		{ID: "row-1", Kind: animation.KindTitle, Bounds: emuBox(160, 240, 560, 80)},
		{ID: "row-2", Kind: animation.KindTitle, Bounds: emuBox(160, 380, 560, 80)},
		{ID: "row-3", Kind: animation.KindTitle, Bounds: emuBox(160, 520, 560, 80)},
	}
	els := append([]Element{panel}, rows...)
	tracks := make([]*animation.Track, 0, len(rows))
	for i, row := range rows {
		tracks = append(tracks, &animation.Track{ElementID: row.ID, Kind: row.Kind,
			Clips: []*animation.Clip{clipOf("s"+row.ID, "spotlight", int64(i)*140, 240, "withSegment:seg-1")}})
	}
	rev := revisionOf("teaching-clear", 1, tracks...)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 15000, BaseImage: "pages/p0.png",
		Elements: els, SegmentOffsets: map[string]int64{"seg-1": 0}, Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 3 {
		t.Fatalf("fallback shared panel must keep 3 looks, got %d: %+v", len(page.Focus), page.Focus)
	}
	for i, want := range []string{"row-1", "row-2", "row-3"} {
		if page.Focus[i].ElementID != want {
			t.Fatalf("look %d = %s, want %s", i, page.Focus[i].ElementID, want)
		}
	}
}

func TestMergeGazeLooksDoesNotCollapseDifferentGroups(t *testing.T) {
	r := Box{X: 64, Y: 194, W: 760, H: 620}
	spans := []FocusSpan{
		{StartMs: 0, ElementID: "row-1", Rect: &r, group: "row-1"},
		{StartMs: 5000, ElementID: "row-2", Rect: &r, group: "row-2"},
		{StartMs: 10000, ElementID: "row-3", Rect: &r, group: "row-3"},
	}
	got := mergeGazeLooks(spans, 15000, true)
	if len(got) != 3 {
		t.Fatalf("different groups sharing a rect must stay separate, got %d: %+v", len(got), got)
	}
	got = dropContainedLooks(got)
	if len(got) != 3 {
		t.Fatalf("dropContainedLooks cross-group must not drop, got %d: %+v", len(got), got)
	}
}

func TestFocusFromBeatSheetKeepsSiblingsInSharedPanel(t *testing.T) {
	c := newTestCompiler(t)
	panel := Element{ID: "panel", Kind: animation.KindShape, Bounds: emuBox(80, 180, 760, 620)}
	rows := []Element{
		{ID: "row-1", Kind: animation.KindTitle, Bounds: emuBox(160, 240, 560, 80), Text: "行业趋势"},
		{ID: "row-2", Kind: animation.KindTitle, Bounds: emuBox(160, 380, 560, 80), Text: "AI 大模型"},
		{ID: "row-3", Kind: animation.KindTitle, Bounds: emuBox(160, 520, 560, 80), Text: "智能体"},
	}
	els := append([]Element{panel}, rows...)
	tracks := make([]*animation.Track, 0, len(rows))
	beats := make([]animation.Beat, 0, len(rows))
	for i, row := range rows {
		tracks = append(tracks, &animation.Track{ElementID: row.ID, Kind: row.Kind,
			Clips: []*animation.Clip{clipOf("s"+row.ID, "spotlight", 0, 240, "withSegment:seg-1")}})
		beats = append(beats, animation.Beat{
			StartMs: int64(i) * 5000, EndMs: int64(i+1) * 5000,
			SegmentID: "seg-1", ShapeID: row.ID, Shot: animation.BeatShotGaze, Text: row.Text,
		})
	}
	rev := revisionOf("teaching-clear", 1, tracks...)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 15000, BaseImage: "pages/p0.png",
		Elements: els, SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision: rev, BeatSheet: &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: beats},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 3 {
		t.Fatalf("shared panel must keep 3 sibling looks, got %d: %+v", len(page.Focus), page.Focus)
	}
	for i, want := range []string{"row-1", "row-2", "row-3"} {
		if page.Focus[i].ElementID != want {
			t.Fatalf("look %d = %s, want %s", i, page.Focus[i].ElementID, want)
		}
	}
	if page.Focus[1].StartMs != 5000 {
		t.Fatalf("second look start = %d, want 5000", page.Focus[1].StartMs)
	}
}

func TestT8EntranceIgnoresBeatSheet(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "fadeIn", 140, 400, "withSegment:seg-1")}},
		&animation.Track{ElementID: "c", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c3", "fadeIn", 280, 400, "withSegment:seg-1")}},
	)
	rev.MotionMode = animation.MotionModeEntrance
	elems := []Element{
		{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
		{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		{ID: "c", Kind: animation.KindBullet, Bounds: emuBox(96, 400, 800, 80)},
	}
	in := PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 30_000, BaseImage: "pages/p0.png",
		Elements: elems, SegmentOffsets: map[string]int64{"seg-1": 0}, Revision: rev,
	}
	without, _, err := c.CompilePage(in)
	if err != nil {
		t.Fatal(err)
	}
	in.BeatSheet = &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 0, ShapeID: "a", Shot: animation.BeatShotGaze},
		{StartMs: 5000, ShapeID: "b", Shot: animation.BeatShotGaze},
	}}
	withSheet, _, err := c.CompilePage(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(without.Layers) != len(withSheet.Layers) {
		t.Fatalf("layers %d vs %d", len(without.Layers), len(withSheet.Layers))
	}
	starts := func(p Page) map[string]int64 {
		out := map[string]int64{}
		for _, ly := range p.Layers {
			if len(ly.Clips) > 0 {
				out[ly.ElementID] = ly.Clips[0].StartMs
			}
		}
		return out
	}
	a, b := starts(without), starts(withSheet)
	if a["a"] != 0 || a["b"] != 10_000 || a["c"] != 20_000 {
		t.Fatalf("entrance spread = %v, want 0/10000/20000", a)
	}
	if a["a"] != b["a"] || a["b"] != b["b"] || a["c"] != b["c"] {
		t.Fatalf("BeatSheet must not change entrance layers: %v vs %v", a, b)
	}
}

func TestT8bGazeEntranceOnlySpreadsEntranceTracks(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("a-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("a-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("b-spot", "spotlight", 140, 240, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	origSpotB := rev.Tracks[1].Clips[0].StartMs
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: []animation.Beat{
		{StartMs: 3000, ShapeID: "a", Shot: animation.BeatShotGaze},
		{StartMs: 9000, ShapeID: "b", Shot: animation.BeatShotGaze},
	}}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 20000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
		BeatSheet:      sheet,
		Focus:          nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 2 {
		t.Fatalf("focus = %d, want 2: %+v", len(page.Focus), page.Focus)
	}
	if page.Focus[0].StartMs != 3000 || page.Focus[1].StartMs != 9000 {
		t.Fatalf("focus times = %d,%d want beat t0 3000,9000", page.Focus[0].StartMs, page.Focus[1].StartMs)
	}
	if len(page.Layers) != 1 || page.Layers[0].ElementID != "a" {
		t.Fatalf("layers = %+v, want only fadeIn on a", page.Layers)
	}
	if page.Layers[0].Clips[0].StartMs != 3000 {
		t.Fatalf("fadeIn StartMs = %d, want first Focus 3000", page.Layers[0].Clips[0].StartMs)
	}
	if rev.Tracks[1].Clips[0].StartMs != origSpotB {
		t.Fatalf("caller spotlight StartMs mutated: %d", rev.Tracks[1].Clips[0].StartMs)
	}
}

func TestGazeFromSegmentTracksHonorsDenyIDs(t *testing.T) {
	rev := revisionOf("teaching-clear", 1)
	rev.MotionMode = animation.MotionModeGaze
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6", "pn"}
	tracks := make([]*animation.Track, len(ids))
	elems := make([]Element, len(ids))
	for i, id := range ids {
		tracks[i] = &animation.Track{ElementID: id, Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("s"+id, "spotlight", int64(i)*140, 240, "withSegment:seg-1")}}
		if id == "pn" {
			elems[i] = Element{ID: id, Kind: animation.KindBullet, Bounds: emuBox(1720, 990, 112, 64)}
			continue
		}
		row, col := i/3, i%3
		elems[i] = Element{ID: id, Kind: animation.KindBullet,
			Bounds: emuBox(int64(64+col*442), int64(194+row*244), 429, 232)}
	}
	rev.Tracks = tracks
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 80000, BaseImage: "pages/p0.png",
		Elements: elems, SegmentOffsets: map[string]int64{"seg-1": 0}, Revision: rev,
		GazeDenyIDs: map[string]struct{}{"pn": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 6 {
		t.Fatalf("focus = %d, want 6 (no footer): %+v", len(page.Focus), page.Focus)
	}
	for _, f := range page.Focus {
		if f.ElementID == "pn" {
			t.Fatal("denied page number became a look")
		}
	}
}

func TestAIPage1BeatSheetDropsFooterAndKeepsSixCards(t *testing.T) {
	c := newTestCompiler(t)
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6"}
	names := []string{"智慧城市", "智能办公", "智能制造", "数字政务", "智慧医疗", "产业升级"}
	pos := [][4]int64{
		{64, 194, 429, 232}, {506, 194, 429, 232}, {947, 194, 429, 232},
		{64, 438, 429, 232}, {506, 438, 429, 232}, {947, 438, 429, 232},
	}
	elems := make([]Element, 0, 7)
	tracks := make([]*animation.Track, 0, 7)
	beats := make([]animation.Beat, 0, 6)
	for i, id := range ids {
		elems = append(elems, Element{ID: id, Kind: animation.KindTitle, Bounds: emuBox(pos[i][0], pos[i][1], pos[i][2], pos[i][3])})
		tracks = append(tracks, &animation.Track{ElementID: id, Kind: animation.KindTitle, Clips: []*animation.Clip{
			clipOf("s"+id, "spotlight", int64(i)*140, 240, "withSegment:seg-1"),
		}})
		beats = append(beats, animation.Beat{
			StartMs: int64(i) * 10000, ShapeID: id, Shot: animation.BeatShotGaze, SegmentID: "seg-1",
		})
		_ = names
	}
	elems = append(elems, Element{ID: "pn", Kind: animation.KindBullet, Bounds: emuBox(1720, 990, 112, 64)})
	tracks = append(tracks, &animation.Track{ElementID: "pn", Kind: animation.KindBullet, Clips: []*animation.Clip{
		clipOf("spn", "spotlight", 840, 240, "withSegment:seg-1"),
	}})
	rev := revisionOf("teaching-clear", 1, tracks...)
	rev.MotionMode = animation.MotionModeGaze
	sheet := &animation.BeatSheet{Source: animation.BeatSourcePunct, Beats: beats}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 80000, BaseImage: "pages/p0.png",
		Elements: elems, SegmentOffsets: map[string]int64{"seg-1": 0}, Revision: rev,
		BeatSheet: sheet, GazeDenyIDs: map[string]struct{}{"pn": {}}, Focus: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 6 {
		t.Fatalf("page-1 looks=%d want 6: %+v", len(page.Focus), page.Focus)
	}
	for _, f := range page.Focus {
		if f.ElementID == "pn" {
			t.Fatal("footer became a look")
		}
		if f.Rect != nil && f.Rect.X >= 1700 && f.Rect.Y >= 980 && f.Rect.W <= 130 && f.Rect.H <= 80 {
			t.Fatalf("footer-sized rect survived: %+v", f.Rect)
		}
	}
	if page.Focus[1].ElementID != "c2" || page.Focus[1].StartMs != 10000 {
		t.Fatalf("智能办公 look = %+v, want c2@10000", page.Focus[1])
	}
	for i := 1; i < len(page.Focus); i++ {
		if page.Focus[i].StartMs <= page.Focus[i-1].StartMs {
			t.Fatalf("focus not time-ordered: %d then %d", page.Focus[i-1].StartMs, page.Focus[i].StartMs)
		}
	}
}

func TestPlanMotionSpotlightStaysHardError(t *testing.T) {
	if _, err := animation.PlanMotion("spotlight", animation.Params{}, "easeOut", 1); err == nil {
		t.Fatal("PlanMotion(spotlight) must stay a hard error")
	}
}

func TestCompileBusinessCalmDoesNotInventFocus(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 6000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0, "seg-2": 2000},
		Revision: revisionOf("business-calm", 1,
			&animation.Track{ElementID: "a", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "withSegment:seg-1")}},
			&animation.Track{ElementID: "b", Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf("c2", "fadeIn", 0, 400, "withSegment:seg-2")}},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 0 {
		t.Fatalf("business-calm must not invent focus, got %+v", page.Focus)
	}
}

func TestCompileGazeEntranceSkippedOnOverlap(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "card", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "withSegment:seg-1"),
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "note", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c2-spot", "spotlight", 140, 240, "withSegment:seg-1"),
			clipOf("c2-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "card", Kind: animation.KindBullet, Bounds: emuBox(0, 0, 400, 300)},
			{ID: "note", Kind: animation.KindBullet, Bounds: emuBox(300, 150, 400, 300)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiag(diags, DiagEntranceSkipped) {
		t.Fatalf("缺 %s 诊断，got %+v", DiagEntranceSkipped, diags)
	}
	if len(page.Layers) != 0 {
		t.Fatalf("overlap must skip entrance layers, got %d", len(page.Layers))
	}
	if len(page.Focus) < 2 {
		t.Fatalf("gaze focus must remain, got %+v", page.Focus)
	}
	for i, f := range page.Focus {
		if f.Rect == nil || f.Rect.W <= 0 {
			t.Fatalf("focus[%d] missing rect: %+v", i, f)
		}
	}
}

func TestCompileGazeEntranceSkippedOnFullPageBox(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1, &animation.Track{
		ElementID: "hero", Kind: animation.KindTitle, Clips: []*animation.Clip{
			clipOf("c-spot", "spotlight", 0, 240, "onEnter"),
			clipOf("c-fade", "fadeIn", 0, 400, "onEnter"),
		},
	})
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 6000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "hero", Kind: animation.KindTitle, Bounds: emuBox(0, 0, 1920, 1080)},
		},
		Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiag(diags, DiagEntranceSkipped) {
		t.Fatalf("缺 %s 诊断，got %+v", DiagEntranceSkipped, diags)
	}
	if len(page.Layers) != 0 {
		t.Fatalf("full-page box must not punch, got %d layers", len(page.Layers))
	}
}

func TestCompileGazeLeftoverFadeInSkipped(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("teaching-clear", 1, &animation.Track{
		ElementID: "a", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c-fade", "fadeIn", 0, 400, "withSegment:seg-1"),
		},
	})
	rev.MotionMode = animation.MotionModeGaze
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 8000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasDiag(diags, DiagEntranceSkipped) {
		t.Fatalf("pure gaze leftover fadeIn is skipped silently, got %+v", diags)
	}
	if len(page.Layers) != 0 {
		t.Fatalf("gaze leftover fadeIn must not become layers, got %d", len(page.Layers))
	}
}

func TestCompileGazeEntranceSpreadAlignsPairedClips(t *testing.T) {
	c := newTestCompiler(t)
	ids := []string{"a", "b", "c"}
	tracks := make([]*animation.Track, 3)
	elems := make([]Element, 3)
	for i, id := range ids {
		tracks[i] = &animation.Track{ElementID: id, Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf(id+"-spot", "spotlight", int64(i)*140, 240, "withSegment:seg-1"),
			clipOf(id+"-fade", "fadeIn", int64(i)*140, 400, "withSegment:seg-1"),
		}}
		elems[i] = Element{ID: id, Kind: animation.KindBullet, Bounds: emuBox(96, int64(200+i*120), 800, 80)}
	}
	rev := revisionOf("teaching-clear", 1, tracks...)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 30000, BaseImage: "pages/p0.png",
		Elements: elems, SegmentOffsets: map[string]int64{"seg-1": 0}, Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasDiag(diags, DiagEntranceSkipped) {
		t.Fatalf("reliable boxes must compile entrance, got %+v", diags)
	}
	if len(page.Layers) != 3 {
		t.Fatalf("layers = %d, want 3", len(page.Layers))
	}
	want := []int64{0, 10000, 20000}
	got := map[string]int64{}
	for _, layer := range page.Layers {
		if len(layer.Clips) != 1 || layer.Clips[0].Effect != "fadeIn" {
			t.Fatalf("layer %s clips = %+v, want one fadeIn", layer.ElementID, layer.Clips)
		}
		got[layer.ElementID] = layer.Clips[0].StartMs
	}
	for i, id := range ids {
		if got[id] != want[i] {
			t.Fatalf("%s start = %d, want %d (paired spotlight+fadeIn must share the spread slot)", id, got[id], want[i])
		}
	}
	if len(page.Focus) != 3 {
		t.Fatalf("focus = %d, want 3", len(page.Focus))
	}
	for i, f := range page.Focus {
		if f.StartMs != want[i] {
			t.Fatalf("focus[%d].StartMs = %d, want %d", i, f.StartMs, want[i])
		}
	}
	if !isGazeLookPage(page) {
		t.Fatal("compiled gaze+entrance with Focus.rect must stay on the look chain")
	}
}

func TestCompileGazeEntranceSkipsChartTable(t *testing.T) {
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
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 1 || page.Layers[0].ElementID != "a" {
		t.Fatalf("layers = %+v, want only bullet a", page.Layers)
	}
}

func TestCompileTechDynamicStaysFullPage(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "a", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "spotlight", 0, 240, "withSegment:seg-1")}},
		&animation.Track{ElementID: "b", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "spotlight", 140, 240, "withSegment:seg-1")}},
	)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 80000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindBullet, Bounds: emuBox(96, 200, 800, 80)},
			{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(96, 300, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.LookCrop {
		t.Fatal("tech-dynamic catalog lookCrop is vetoed: lecture pages stay full-page")
	}
	if page.LookCropPadPx != 0 {
		t.Fatalf("pad = %d, want 0", page.LookCropPadPx)
	}
	if !isGazeLookPage(page) {
		t.Fatal("full-page tech gaze still uses the look chain")
	}
	if len(page.Annotations) == 0 {
		t.Fatal("full-page tech gaze keeps callout")
	}
	if len(page.Layers) != 0 {
		t.Fatalf("tech gaze must not emit layers, got %d", len(page.Layers))
	}
}

func TestCompileTechDynamicOmitsLookCropWithoutFocus(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1,
		&animation.Track{ElementID: "a", Kind: animation.KindShape,
			Clips: []*animation.Clip{
				clipOf("c1", "slideIn", 0, 240, "withSegment:seg-1"),
				clipOf("c2", "spotlight", 0, 240, "withSegment:seg-1"),
			}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, _, err := c.CompilePage(PageInput{
		Index: 22, SlideID: "slide-278", DurationMs: 31840, BaseImage: "pages/p22.png",
		Elements: []Element{
			{ID: "a", Kind: animation.KindShape, Bounds: emuBox(96, 200, 800, 80)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 0 {
		t.Fatalf("focus = %+v, want none", page.Focus)
	}
	if page.LookCrop {
		t.Fatal("lookCrop without Focus.rect is invalid_plan and drops the whole film to static")
	}
	plan := &Timeline{
		SchemaVersion: IRSchemaVersion,
		FPS:           30,
		Width:         1920,
		Height:        1080,
		Background:    "#FFFFFF",
		Pages:         []Page{page},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("single-layer tech page must be a valid plan: %v", err)
	}
}

func TestLayerElementIDsSkipsUnreliableGazeEntrance(t *testing.T) {
	elems := []Element{
		{ID: "card", Kind: animation.KindBullet, Bounds: emuBox(0, 0, 400, 300)},
		{ID: "note", Kind: animation.KindBullet, Bounds: emuBox(300, 150, 400, 300)},
	}
	rev := revisionOf("teaching-clear", 1,
		&animation.Track{ElementID: "card", Kind: animation.KindBullet, Clips: []*animation.Clip{clipOf("a", "fadeIn", 0, 400, "onEnter")}},
		&animation.Track{ElementID: "note", Kind: animation.KindBullet, Clips: []*animation.Clip{clipOf("b", "fadeIn", 0, 400, "onEnter")}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	got := LayerElementIDs(rev, elems, design.Canvas{Width: 1920, Height: 1080}, emuHeight)
	if got != nil {
		t.Fatalf("unreliable boxes must not count as layers, got %v", got)
	}
}

func TestCalloutShapeAutoIsRect(t *testing.T) {
	square := Box{X: 100, Y: 200, W: 400, H: 400}
	if got := calloutShape(square); got != AnnotationShapeRect {
		t.Fatalf("square auto = %q, want rect", got)
	}
	wide := Box{X: 0, Y: 0, W: 800, H: 80}
	if got := calloutShape(wide); got != AnnotationShapeRect {
		t.Fatalf("wide auto = %q, want rect", got)
	}
	anns := annotationsFromFocus([]FocusSpan{
		{StartMs: 0, ElementID: "card", Rect: &square, FadeMs: 240},
		{StartMs: 4000, ElementID: "next", Rect: &wide, FadeMs: 240},
	}, "auto")
	if len(anns) != 2 || anns[0].Shape != AnnotationShapeRect {
		t.Fatalf("auto callout shape = %+v, want rect", anns)
	}
	forced := annotationsFromFocus([]FocusSpan{
		{StartMs: 0, ElementID: "card", Rect: &square, FadeMs: 240},
		{StartMs: 4000, ElementID: "next", Rect: &wide, FadeMs: 240},
	}, "circle")
	if forced[0].Shape != AnnotationShapeCircle {
		t.Fatalf("explicit circle = %q", forced[0].Shape)
	}
}

func TestGazeSkipsPageTitleKeepsCards(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "heading", Kind: animation.KindTitle, Clips: []*animation.Clip{
			clipOf("h-spot", "spotlight", 0, 240, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "c1", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c1-spot", "spotlight", 140, 240, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "c2", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("c2-spot", "spotlight", 280, 240, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGazeEntrance
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 60000, BaseImage: "pages/p0.png",
		Elements: []Element{
			{ID: "heading", Kind: animation.KindTitle, Bounds: emuBox(96, 48, 1440, 96)},
			{ID: "c1", Kind: animation.KindBullet, Bounds: emuBox(96, 220, 420, 280)},
			{ID: "c2", Kind: animation.KindBullet, Bounds: emuBox(596, 220, 420, 280)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Focus) != 2 {
		t.Fatalf("focus = %d, want 2 cards (no page title): %+v", len(page.Focus), page.Focus)
	}
	for _, f := range page.Focus {
		if f.ElementID == "heading" {
			t.Fatal("page title became a look")
		}
	}
	if len(page.ClearRects) != 1 {
		t.Fatalf("clearRects = %d, want 1 page heading: %+v", len(page.ClearRects), page.ClearRects)
	}
	got := page.ClearRects[0]
	if got.Y >= 1080/4 || got.W <= 1920*2/5 {
		t.Fatalf("heading clear rect %+v is not a wide top bar", got)
	}
}

func TestHeadingClearRectsBulletTitleAndCornerTag(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "tag", Kind: animation.KindTitle, Clips: []*animation.Clip{
			clipOf("tag-spot", "spotlight", 0, 240, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "heading", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("h-spot", "spotlight", 40, 240, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "kicker", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("k-spot", "spotlight", 80, 240, "withSegment:seg-1"),
		}},
		&animation.Track{ElementID: "metric", Kind: animation.KindBullet, Clips: []*animation.Clip{
			clipOf("m-spot", "spotlight", 200, 240, "withSegment:seg-1"),
		}},
	)
	rev.MotionMode = animation.MotionModeGaze
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-258", DurationMs: 21080, BaseImage: "pages/p1.png",
		Elements: []Element{
			{ID: "tag", Kind: animation.KindTitle, Bounds: emuBox(1344, 23, 480, 48)},
			{ID: "heading", Kind: animation.KindBullet, Bounds: emuBox(96, 58, 1728, 115)},
			{ID: "kicker", Kind: animation.KindBullet, Bounds: emuBox(96, 173, 1728, 67)},
			{ID: "metric", Kind: animation.KindBullet, Bounds: emuBox(96, 299, 410, 170)},
		},
		SegmentOffsets: map[string]int64{"seg-1": 0},
		Revision:       rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.ClearRects) != 2 {
		t.Fatalf("clearRects = %d, want tag + page title: %+v", len(page.ClearRects), page.ClearRects)
	}
	var sawTitle, sawTag, sawKicker bool
	for _, r := range page.ClearRects {
		if r.Y <= 30 && r.W >= 470 {
			sawTag = true
		}
		if r.Y >= 50 && r.Y < 80 && r.W >= 1700 {
			sawTitle = true
		}
		if r.Y >= 160 {
			sawKicker = true
		}
	}
	if !sawTitle || !sawTag {
		t.Fatalf("clearRects = %+v, want corner tag and 大会概览-sized title", page.ClearRects)
	}
	if sawKicker {
		t.Fatalf("subtitle/kicker must stay dimmable: %+v", page.ClearRects)
	}
	for _, f := range page.Focus {
		if f.ElementID == "heading" || f.ElementID == "tag" {
			t.Fatalf("page chrome became a look: %+v", page.Focus)
		}
	}
}

func TestDropContainedLooksPrefersCardOverTitle(t *testing.T) {
	title := Box{X: 120, Y: 220, W: 360, H: 48}
	card := Box{X: 96, Y: 200, W: 420, H: 280}
	titleB := Box{X: 620, Y: 220, W: 360, H: 48}
	cardB := Box{X: 596, Y: 200, W: 420, H: 280}
	got := dropContainedLooks([]FocusSpan{
		{StartMs: 0, ElementID: "title-a", Rect: &title},
		{StartMs: 2000, ElementID: "body-a", Rect: &card},
		{StartMs: 4000, ElementID: "title-b", Rect: &titleB},
		{StartMs: 6000, ElementID: "body-b", Rect: &cardB},
	})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 card looks: %+v", len(got), got)
	}
	if got[0].ElementID != "body-a" || got[1].ElementID != "body-b" {
		t.Fatalf("ids = %s,%s want body-a,body-b", got[0].ElementID, got[1].ElementID)
	}
}
