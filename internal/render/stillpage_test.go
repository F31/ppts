package render

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func stillPlan() *Timeline {
	return &Timeline{
		FPS: 30, Width: 1920, Height: 1080, Background: "#FFFFFF",
		Pages: []Page{
			{Index: 0, DurationMs: 2000, BaseImage: "pages/p0.png", Transition: Transition{Type: animation.TransitionNone}},
			{Index: 1, DurationMs: 3000, BaseImage: "pages/p1.png", Transition: Transition{Type: animation.TransitionFade, DurationMs: 240}},
			{
				Index: 2, DurationMs: 3000, BaseImage: "pages/p2.png",
				Transition: Transition{Type: animation.TransitionSlide, DurationMs: 240},
			},
			{
				Index: 3, DurationMs: 3000, BaseImage: "pages/p3.png",
				Transition: Transition{Type: animation.TransitionFade, DurationMs: 240},
				Layers: []Layer{{
					ElementID: "shape-1",
					Clips:     []Motion{{DurationMs: 400, Props: []animation.MotionProp{{Name: animation.PropOpacity, From: 0, To: 1}}}},
				}},
			},
			{
				Index: 4, DurationMs: 3000, BaseImage: "pages/p4.png",
				Focus: []FocusSpan{{StartMs: 0, ElementID: "shape-1"}},
			},
			{
				Index: 5, DurationMs: 3000, BaseImage: "pages/p5.png",
				Shots: []Shot{{Index: 0, Image: "pages/p5.png", DurationMs: 3000}},
			},
			{
				Index: 6, DurationMs: 3000, BaseImage: "pages/p6.png",
				Layers: []Layer{{
					Clips: []Motion{{DurationMs: 400, Props: []animation.MotionProp{{Name: animation.PropTranslateY, From: 24, To: 0}}}},
				}},
			},
		},
	}
}

func TestPageNeedsCompositorOnlyForElementMotion(t *testing.T) {
	plan := stillPlan()
	want := []bool{false, false, true, true, true, true, true}
	for i, page := range plan.Pages {
		if got := pageNeedsCompositor(page); got != want[i] {
			t.Errorf("page %d compositor = %v, want %v", i, got, want[i])
		}
	}
}

func TestPageNeedsCompositorGazeLookLeavesChrome(t *testing.T) {
	p := Page{
		DurationMs: 80000, BaseImage: "pages/p0.png",
		Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80},
		}},
	}
	if HasEntranceElementLayer(p.Layers) {
		t.Fatal("gaze page has no entrance layer")
	}
	if pageNeedsCompositor(p) {
		t.Fatal("gaze+fade+Focus.rect must not stay on the compositor")
	}
	if !isGazeLookPage(p) {
		t.Fatal("expected a look-chain page")
	}
}

func TestSplitRenderPagesKeepsOrder(t *testing.T) {
	chrome, gaze, still := splitRenderPages(stillPlan(), []int{6, 1, 0, 3})
	if len(gaze) != 0 {
		t.Fatalf("gaze = %v, want empty", gaze)
	}
	if strings.Trim(joinInts(chrome), " ") != "6,3" {
		t.Fatalf("chrome = %v, want 6,3", chrome)
	}
	if strings.Trim(joinInts(still), " ") != "1,0" {
		t.Fatalf("still = %v, want 1,0", still)
	}
}

func TestSplitRenderPagesGazeNotStill(t *testing.T) {
	plan := &Timeline{
		FPS: 30, Width: 1920, Height: 1080,
		Pages: []Page{
			{Index: 0, DurationMs: 80000, BaseImage: "pages/p0.png",
				Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
				Focus:      []FocusSpan{{StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80}}}},
			{Index: 1, DurationMs: 3000, BaseImage: "pages/p1.png",
				Transition: Transition{Type: animation.TransitionNone}},
			{Index: 2, DurationMs: 3000, BaseImage: "pages/p2.png",
				Layers: []Layer{{ElementID: "e", Clips: []Motion{{Effect: "fadeIn", DurationMs: 400}}}},
			},
		},
	}
	chrome, gaze, still := splitRenderPages(plan, []int{0, 1, 2})
	if strings.Trim(joinInts(gaze), " ") != "0" {
		t.Fatalf("gaze = %v, want 0", gaze)
	}
	if strings.Trim(joinInts(still), " ") != "1" {
		t.Fatalf("still = %v, want 1 (must not swallow the gaze page)", still)
	}
	if strings.Trim(joinInts(chrome), " ") != "2" {
		t.Fatalf("chrome = %v, want 2", chrome)
	}
}

func TestSplitRenderPagesGazeEntranceStaysOnLookChain(t *testing.T) {
	plan := &Timeline{
		FPS: 30, Width: 1920, Height: 1080,
		Pages: []Page{{
			Index: 0, DurationMs: 8000, BaseImage: "pages/p0.png",
			Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
			Focus:      []FocusSpan{{StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80}}},
			Layers:     []Layer{{ElementID: "a", Clips: []Motion{{Effect: "fadeIn", DurationMs: 400}}}},
		}},
	}
	chrome, gaze, still := splitRenderPages(plan, []int{0})
	if strings.Trim(joinInts(gaze), " ") != "0" {
		t.Fatalf("gaze = %v, want 0", gaze)
	}
	if len(chrome) != 0 || len(still) != 0 {
		t.Fatalf("gaze+entrance with Focus.rect stays on the look chain: chrome=%v still=%v", chrome, still)
	}
}

func TestSplitRenderPagesNonEntranceClipGoesChrome(t *testing.T) {
	plan := &Timeline{
		FPS: 30, Width: 1920, Height: 1080,
		Pages: []Page{{
			Index: 0, DurationMs: 8000, BaseImage: "pages/p0.png",
			Focus:  []FocusSpan{{StartMs: 0, ElementID: "a", Rect: &Box{X: 10, Y: 10, W: 200, H: 80}}},
			Layers: []Layer{{ElementID: "a", Clips: []Motion{{Effect: "bob", DurationMs: 2000}}}},
		}},
	}
	chrome, gaze, still := splitRenderPages(plan, []int{0})
	if strings.Trim(joinInts(chrome), " ") != "0" {
		t.Fatalf("chrome = %v, want 0", chrome)
	}
	if len(gaze) != 0 || len(still) != 0 {
		t.Fatalf("bob must stay on Chrome: gaze=%v still=%v", gaze, still)
	}
}

func TestFramesForDurationMatchesRenderer(t *testing.T) {
	if got := framesForDuration(240, 30); got != 7 {
		t.Fatalf("240ms at 30fps = %d frames, want 7", got)
	}
	if got := framesForDuration(2000, 30); got != 60 {
		t.Fatalf("2000ms at 30fps = %d frames, want 60", got)
	}
	if got := framesForDuration(0, 30); got != 1 {
		t.Fatalf("zero duration = %d frames, want 1", got)
	}
}

func TestStillEncodeArgsHold(t *testing.T) {
	args, err := stillEncodeArgs(stillEncodeSpec{
		Width: 1920, Height: 1080, FPS: 30, CRF: 18, GOP: 30,
		Frames: 60, Background: "#112233",
		Image: "page.png", Out: "segment_000.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-i\npage.png",
		"-frames:v\n60",
		"-c:v\nlibx264",
		"-pix_fmt\nyuv420p",
		"-crf\n18",
		"-g\n30",
		"-an",
		"-colorspace\nbt709",
		"-color_range\ntv",
		"color=0x112233",
		"segment_000.mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "xfade") {
		t.Fatalf("a hold must not crossfade:\n%s", joined)
	}
}

func TestStillEncodeArgsFade(t *testing.T) {
	args, err := stillEncodeArgs(stillEncodeSpec{
		Width: 1440, Height: 1080, FPS: 30, Frames: 60, TransitionFrames: 7,
		Image: "page.png", PrevImage: "prev.png", Out: "segment_001.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-i\nprev.png",
		"-i\npage.png",
		"xfade=transition=custom:duration=0.233:offset=0",
		`expr='A*pow(1-P\,3)+B*(1-pow(1-P\,3))'`,
		"fps=30:start_time=0",
		"-frames:v\n60",
		"-colorspace\nbt709",
		"-an",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
}

func TestHoldTailArgsClonesLastFrameToFullDuration(t *testing.T) {
	args, err := holdTailArgs(holdTailSpec{
		MotionFrames: 13, TotalFrames: 600, FPS: 30, CRF: 18, GOP: 30,
		In: "segment_000.mp4", Out: "segment_000.hold.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-i\nsegment_000.mp4",
		"tpad=stop_mode=clone:stop_duration=",
		"-frames:v\n600",
		"-pix_fmt\nyuv420p",
		"-crf\n18",
		"-g\n30",
		"-colorspace\nbt709",
		"-color_primaries\nbt709",
		"-color_trc\nbt709",
		"-color_range\ntv",
		"segment_000.hold.mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
}

func TestStillEncodeArgsRejectsOddCanvas(t *testing.T) {
	if _, err := stillEncodeArgs(stillEncodeSpec{Width: 1919, Height: 1080, Frames: 1, Image: "a.png", Out: "o.mp4"}); err == nil {
		t.Fatal("odd width must be rejected")
	}
}
