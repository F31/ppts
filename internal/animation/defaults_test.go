package animation

import (
	"reflect"
	"testing"

	"github.com/F31/ppts/internal/project"
)

func samplePage() *project.Page {
	box := func(x, y, w, h int64) *project.Box {
		return &project.Box{X: x, Y: y, Width: w, Height: h}
	}
	return &project.Page{
		Index:   2,
		SlideID: "slide-3",
		Shapes: []*project.Shape{
			{ID: "shape-title", Kind: "text", Text: "第三季度经营回顾", Bounds: box(800000, 600000, 8000000, 900000)},
			{ID: "shape-b1", Kind: "text", Text: "收入同比增长 18%", Bounds: box(800000, 2000000, 7000000, 600000)},
			{ID: "shape-img", Kind: "picture", Bounds: box(9000000, 2000000, 3000000, 2000000)},
			{ID: "shape-chart", Kind: "chart", ChartType: "bar", Bounds: box(1000000, 4300000, 6000000, 3000000)},
			{ID: "shape-b2", Kind: "text", Text: "毛利率 42%", Bounds: box(800000, 2800000, 7000000, 600000)},
			{ID: "shape-b3", Kind: "text", Text: "新增客户 36 家", Bounds: box(800000, 3600000, 7000000, 600000)},
			{ID: "shape-nobounds", Kind: "text", Text: "无包围盒的形状"},
			{ID: "shape-audio", Kind: "audio", Bounds: box(0, 0, 100, 100)},
			{ID: "shape-unknown", Kind: "opaque", Opaque: true, Bounds: box(0, 0, 100, 100)},
		},
	}
}

func defaultOptions(preset string) DefaultOptions {
	return DefaultOptions{
		SlideID:    "slide-3",
		Language:   "zh-CN",
		DurationMS: 6000,
		FPS:        30,
		SegmentIDs: []string{"seg-1", "seg-2"},
		PresetID:   preset,
	}
}

func TestDefaultSpecPassesValidationForEveryPreset(t *testing.T) {
	for _, preset := range Catalog().Presets() {
		t.Run(preset.ID, func(t *testing.T) {
			rev, err := DefaultSpec(samplePage(), defaultOptions(preset.ID))
			if err != nil {
				t.Fatalf("DefaultSpec(%s): %v", preset.ID, err)
			}
			if rev.PresetID != preset.ID {
				t.Fatalf("preset = %s, want %s", rev.PresetID, preset.ID)
			}
			if rev.Intensity != preset.Intensity {
				t.Fatalf("intensity = %v, want %v", rev.Intensity, preset.Intensity)
			}
			if rev.Status != StatusDraft {
				t.Fatalf("status = %s, want draft", rev.Status)
			}
			pc := PageContext{DurationMS: 6000, FPS: 30, Elements: anchorsOf(pageElements(samplePage())), SegmentIDs: []string{"seg-1", "seg-2"}}
			if err := rev.Validate(pc); err != nil {
				t.Fatalf("default spec invalid: %v", err)
			}
			for _, tr := range rev.Tracks {
				for _, c := range tr.Clips {
					if c.Trigger == "" || c.Easing == "" {
						t.Fatalf("clip %s missing trigger/easing", c.ID)
					}
				}
			}
		})
	}
}

func TestDefaultSpecIsDeterministic(t *testing.T) {
	a, err := DefaultSpec(samplePage(), defaultOptions(DefaultPresetID))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	b, err := DefaultSpec(samplePage(), defaultOptions(DefaultPresetID))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same input produced different specs")
	}
}

func TestDefaultSpecSkipsUnusableElements(t *testing.T) {
	rev, err := DefaultSpec(samplePage(), defaultOptions(DefaultPresetID))
	if err != nil {
		t.Fatalf("DefaultSpec: %v", err)
	}
	seen := map[string]string{}
	for _, tr := range rev.Tracks {
		seen[tr.ElementID] = tr.Kind
	}
	for _, id := range []string{"shape-nobounds", "shape-audio", "shape-unknown"} {
		if _, ok := seen[id]; ok {
			t.Errorf("element %s should not get an animation track", id)
		}
	}
	if seen["shape-title"] != KindTitle {
		t.Errorf("title kind = %q, want %q", seen["shape-title"], KindTitle)
	}
	if seen["shape-img"] != KindImage || seen["shape-chart"] != KindChart {
		t.Errorf("image/chart classification wrong: %v", seen)
	}
}

func TestDefaultSpecAlignsBulletsToSegments(t *testing.T) {
	rev, err := DefaultSpec(samplePage(), defaultOptions("business-calm"))
	if err != nil {
		t.Fatalf("DefaultSpec: %v", err)
	}
	var bulletTriggers []string
	for _, tr := range rev.Tracks {
		if tr.Kind != KindBullet {
			continue
		}
		if len(tr.Clips) != 1 {
			t.Fatalf("bullet track has %d clips, want 1", len(tr.Clips))
		}
		bulletTriggers = append(bulletTriggers, tr.Clips[0].Trigger)
	}
	// 采样页有 3 条要点、讲稿只有 2 段：前两条相对锚定，第三条退化为页首触发。
	want := []string{"withSegment:seg-1", "withSegment:seg-2", "onEnter"}
	if !reflect.DeepEqual(bulletTriggers, want) {
		t.Fatalf("bullet triggers = %v, want %v", bulletTriggers, want)
	}
}

func TestDefaultSpecFallsBackToPageTrack(t *testing.T) {
	empty := &project.Page{Index: 0, SlideID: "slide-1"}
	rev, err := DefaultSpec(empty, defaultOptions(DefaultPresetID))
	if err != nil {
		t.Fatalf("DefaultSpec: %v", err)
	}
	if len(rev.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(rev.Tracks))
	}
	if rev.Tracks[0].Kind != KindPage || rev.Tracks[0].ElementID != "" {
		t.Fatalf("fallback track = %+v, want page track", rev.Tracks[0])
	}
	pc := PageContext{DurationMS: 6000, FPS: 30}
	if err := rev.Validate(pc); err != nil {
		t.Fatalf("fallback spec invalid: %v", err)
	}
}

func TestDefaultSpecShortPageStillValid(t *testing.T) {
	opts := defaultOptions(DefaultPresetID)
	opts.DurationMS = 300
	rev, err := DefaultSpec(samplePage(), opts)
	if err != nil {
		t.Fatalf("DefaultSpec: %v", err)
	}
	pc := PageContext{DurationMS: 300, FPS: 30, Elements: anchorsOf(pageElements(samplePage()))}
	if err := rev.Validate(pc); err != nil {
		t.Fatalf("short-page spec invalid: %v", err)
	}
	for _, tr := range rev.Tracks {
		for _, c := range tr.Clips {
			if c.StartMs+c.DurationMs > 300+pc.FrameMS() {
				t.Fatalf("clip %s exceeds page: %d", c.ID, c.StartMs+c.DurationMs)
			}
		}
	}
}

func TestDefaultSpecCoversPageShapes(t *testing.T) {
	box := func(y int64) *project.Box {
		return &project.Box{X: 100000, Y: y, Width: 5000000, Height: 600000}
	}
	cases := map[string]*project.Page{
		"text only": {
			Shapes: []*project.Shape{
				{ID: "t", Kind: "text", Text: "标题", Bounds: box(0)},
				{ID: "b", Kind: "text", Text: "要点一", Bounds: box(1000000)},
			},
		},
		"image only": {
			Shapes: []*project.Shape{
				{ID: "i", Kind: "picture", Bounds: box(0)},
			},
		},
		"table only": {
			Shapes: []*project.Shape{
				{ID: "tb", Kind: "table", TableRows: 2, TableCols: 2, Bounds: box(0)},
			},
		},
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			for _, preset := range Catalog().Presets() {
				rev, err := DefaultSpec(page, defaultOptions(preset.ID))
				if err != nil {
					t.Fatalf("preset %s: %v", preset.ID, err)
				}
				pc := PageContext{DurationMS: 6000, FPS: 30, Elements: anchorsOf(pageElements(page)), SegmentIDs: []string{"seg-1", "seg-2"}}
				if err := rev.Validate(pc); err != nil {
					t.Fatalf("preset %s produced invalid spec: %v", preset.ID, err)
				}
			}
		})
	}
}

func TestDefaultSpecRejectsBadInput(t *testing.T) {
	if _, err := DefaultSpec(nil, defaultOptions(DefaultPresetID)); err == nil {
		t.Fatal("nil page should fail")
	}
	if _, err := DefaultSpec(samplePage(), DefaultOptions{DurationMS: 0}); err == nil {
		t.Fatal("missing duration should fail")
	}
	opts := defaultOptions("no-such-preset")
	if _, err := DefaultSpec(samplePage(), opts); err == nil {
		t.Fatal("unknown preset should fail")
	}
}

func TestDefaultClipIDsAreUnique(t *testing.T) {
	rev, err := DefaultSpec(samplePage(), defaultOptions("launch-bold"))
	if err != nil {
		t.Fatalf("DefaultSpec: %v", err)
	}
	ids := map[string]struct{}{}
	for _, tr := range rev.Tracks {
		for _, c := range tr.Clips {
			if _, dup := ids[c.ID]; dup {
				t.Fatalf("duplicate clip id %q", c.ID)
			}
			ids[c.ID] = struct{}{}
		}
	}
}
