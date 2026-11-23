package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func TestGazePaintOK(t *testing.T) {
	rect := &Box{X: 10, Y: 10, W: 40, H: 20}
	gaze := Page{
		DurationMs: 8000, BaseImage: "pages/p0.png",
		Focus: []FocusSpan{{StartMs: 0, ElementID: "a", Rect: rect}},
	}
	if !isGazeLookPage(gaze) || !gazePaintOK(gaze) {
		t.Fatal("plain gaze page must paint without chrome")
	}
	entrance := gaze
	entrance.Layers = []Layer{{ElementID: "a", Clips: []Motion{{Effect: "fadeIn", DurationMs: 400}}}}
	if gazePaintOK(entrance) {
		t.Fatal("entrance layers must stay on Remotion stills")
	}
	crop := gaze
	crop.LookCrop = true
	if gazePaintOK(crop) {
		t.Fatal("lookCrop punch-in must stay on Remotion")
	}
	flow := gaze
	flow.Overlays = []Overlay{{Kind: "flow", DurationMs: 400, Points: []OverlayPoint{{X: 1, Y: 1}, {X: 2, Y: 2}}}}
	if gazePaintOK(flow) {
		t.Fatal("overlay pages must stay on Remotion")
	}
	blank := gaze
	blank.BaseImage = ""
	if gazePaintOK(blank) {
		t.Fatal("missing base image cannot paint")
	}
	_ = animation.TransitionFade
}

func TestPaintGazeLookPunchesHoleAndCallout(t *testing.T) {
	const w, h = 80, 48
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	blue := color.NRGBA{R: 20, G: 40, B: 180, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetNRGBA(x, y, blue)
		}
	}
	hole := Box{X: 16, Y: 10, W: 24, H: 16}
	page := Page{
		DurationMs: 4000,
		Focus:      []FocusSpan{{StartMs: 0, EndMs: 4000, ElementID: "card", Rect: &hole, FadeMs: 0}},
		ClearRects: []Box{{X: 2, Y: 2, W: 20, H: 8}},
		Annotations: []Annotation{{
			Kind: AnnotationCallout, StartMs: 0, Rect: &hole, Shape: AnnotationShapeRect,
		}},
	}
	got := paintGazeLook(src, page, 2000, w, h)
	inside := got.NRGBAAt(hole.X+hole.W/2, hole.Y+hole.H/2)
	if inside != blue {
		t.Fatalf("hole center = %+v, want original blue", inside)
	}
	title := got.NRGBAAt(8, 5)
	if title != blue {
		t.Fatalf("clearRect = %+v, want original blue", title)
	}
	out := got.NRGBAAt(70, 40)
	if int(out.R) <= int(blue.R)+20 {
		t.Fatalf("veiled pixel %+v should be lighter than blue", out)
	}
	edge := got.NRGBAAt(hole.X, hole.Y)
	if edge.R < 200 || edge.G < 160 {
		t.Fatalf("callout edge %+v want yellow", edge)
	}
}

func TestWritePaintedLookStills(t *testing.T) {
	dir := t.TempDir()
	pageDir := filepath.Join(dir, "pages")
	if err := os.MkdirAll(pageDir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := image.NewNRGBA(image.Rect(0, 0, 64, 36))
	for i := range src.Pix {
		src.Pix[i] = 90
	}
	f, err := os.Create(filepath.Join(pageDir, "p0.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rect := &Box{X: 8, Y: 6, W: 20, H: 12}
	plan := &Timeline{
		FPS: 30, Width: 64, Height: 36,
		Pages: []Page{{
			Index: 0, DurationMs: 2000, BaseImage: "pages/p0.png",
			Focus: []FocusSpan{
				{StartMs: 0, ElementID: "a", Rect: rect},
				{StartMs: 1000, ElementID: "b", Rect: &Box{X: 32, Y: 6, W: 20, H: 12}},
			},
		}},
	}
	if err := writePaintedLookStills(plan, dir, []int{0}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"looks/p000_l00.png", "looks/p000_l01.png"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Size() == 0 {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}
