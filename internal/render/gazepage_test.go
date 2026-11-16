package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func TestLookWindowsFiveSpotlightsOver80s(t *testing.T) {
	page := Page{
		DurationMs: 80000,
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}, FadeMs: 240},
			{StartMs: 16000, ElementID: "b", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}, FadeMs: 240},
			{StartMs: 32000, ElementID: "c", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}, FadeMs: 240},
			{StartMs: 48000, ElementID: "d", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}, FadeMs: 240},
			{StartMs: 64000, ElementID: "e", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}, FadeMs: 240},
		},
	}
	got := lookWindows(page)
	if len(got) != 5 {
		t.Fatalf("looks = %d, want 5: %+v", len(got), got)
	}
	wantStart := []int64{0, 16000, 32000, 48000, 64000}
	wantEnd := []int64{16000, 32000, 48000, 64000, 80000}
	wantFade := []int64{0, 240, 240, 240, 240}
	for i, w := range got {
		if w.StartMs != wantStart[i] || w.EndMs != wantEnd[i] {
			t.Fatalf("look[%d] = [%d,%d), want [%d,%d)", i, w.StartMs, w.EndMs, wantStart[i], wantEnd[i])
		}
		if w.FadeMs != wantFade[i] {
			t.Fatalf("look[%d].FadeMs = %d, want %d", i, w.FadeMs, wantFade[i])
		}
		if w.MidMs < w.StartMs+w.FadeMs || w.MidMs >= w.EndMs {
			t.Fatalf("look[%d] mid %d not in hold [%d,%d)", i, w.MidMs, w.StartMs+w.FadeMs, w.EndMs)
		}
	}
	if got[0].MidMs != 8000 {
		t.Fatalf("first mid = %d, want 8000", got[0].MidMs)
	}
}

func TestLookWindowsIgnoresSpansWithoutRect(t *testing.T) {
	page := Page{
		DurationMs: 4000,
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a"},
			{StartMs: 1000, ElementID: "b", Rect: &Box{X: 1, Y: 1, W: 8, H: 8}},
			{StartMs: 2000, ElementID: "c", Rect: &Box{X: 2, Y: 2, W: 8, H: 8}},
		},
	}
	got := lookWindows(page)
	if len(got) != 3 {
		t.Fatalf("looks = %d, want 3 (unveiled prefix + two rects): %+v", len(got), got)
	}
	if got[0].StartMs != 0 || got[0].EndMs != 1000 || got[0].FadeMs != 0 {
		t.Fatalf("prefix = %+v, want [0,1000) fade 0", got[0])
	}
	if got[0].MidMs != 500 {
		t.Fatalf("prefix MidMs = %d, want 500 (inside the unveiled hold, not page boundary)", got[0].MidMs)
	}
}

func TestLookWindowsPrefixSamplesInsidePage(t *testing.T) {
	page := Page{
		DurationMs: 83840,
		Focus: []FocusSpan{
			{StartMs: 420, ElementID: "c1", Rect: &Box{X: 88, Y: 261, W: 566, H: 304}, FadeMs: 240},
			{StartMs: 20100, ElementID: "c2", Rect: &Box{X: 677, Y: 261, W: 566, H: 304}, FadeMs: 240},
		},
	}
	got := lookWindows(page)
	if len(got) != 3 {
		t.Fatalf("looks = %d, want 3 (prefix + two cards): %+v", len(got), got)
	}
	if got[0].StartMs != 0 || got[0].EndMs != 420 || got[0].FadeMs != 0 {
		t.Fatalf("prefix = %+v, want [0,420) fade 0", got[0])
	}
	if got[0].MidMs <= 0 || got[0].MidMs >= 420 {
		t.Fatalf("prefix MidMs = %d, want inside (0,420)", got[0].MidMs)
	}
	if got[0].MidMs != 210 {
		t.Fatalf("prefix MidMs = %d, want 210", got[0].MidMs)
	}
	if frame := lookFrame(500, 2515, got[0].MidMs, 30); frame <= 500 {
		t.Fatalf("prefix still frame %d must be inside the page (start 500)", frame)
	}
}

func TestGazeClipUsesLookPNGNotBaseImage(t *testing.T) {
	args, err := stillEncodeArgs(stillEncodeSpec{
		Width: 1920, Height: 1080, FPS: 30, CRF: 18, GOP: 30,
		Frames: 480, TransitionFrames: 7,
		Image: "looks/p000_l01.png", PrevImage: "looks/p000_l00.png",
		Out: "looks/p000_l01.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	if strings.Contains(joined, "pages/p0.png") {
		t.Fatal("gaze clip must not encode the base page PNG")
	}
	if !strings.Contains(joined, "looks/p000_l01.png") {
		t.Fatalf("missing look still:\n%s", joined)
	}
	if !strings.Contains(joined, `expr='A*pow(1-P\,3)+B*(1-pow(1-P\,3))'`) {
		t.Fatalf("look xfade must use easeOutCubic:\n%s", joined)
	}
}

func TestLookFrameClampsToPage(t *testing.T) {
	if got := lookFrame(0, 2400, 80000, 30); got != 2399 {
		t.Fatalf("end of 80s page = %d, want 2399", got)
	}
	if got := lookFrame(100, 60, 0, 30); got != 100 {
		t.Fatalf("start = %d, want 100", got)
	}
}

func TestIsGazeLookPageIgnoresLookCrop(t *testing.T) {
	p := Page{
		DurationMs: 5000, BaseImage: "pages/p0.png",
		LookCrop:   true,
		Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
		Focus:      []FocusSpan{{StartMs: 0, ElementID: "a", Rect: &Box{X: 1, Y: 1, W: 20, H: 20}}},
	}
	if !isGazeLookPage(p) {
		t.Fatal("encodeGazePage still takes lookCrop pages; punch-in is PageView")
	}
}

func TestIsGazeLookPageAllowsEntranceRejectsShots(t *testing.T) {
	base := Page{
		DurationMs: 5000,
		Focus:      []FocusSpan{{StartMs: 0, ElementID: "a", Rect: &Box{X: 1, Y: 1, W: 20, H: 20}}},
	}
	withLayer := base
	withLayer.Layers = []Layer{{
		ElementID: "a",
		Clips:     []Motion{{Effect: "fadeIn", DurationMs: 400, Props: []animation.MotionProp{{Name: animation.PropOpacity, From: 0, To: 1}}}},
	}}
	if !isGazeLookPage(withLayer) {
		t.Fatal("gaze+entrance with Focus.rect stays on the look chain")
	}
	withShot := base
	withShot.Shots = []Shot{{Index: 0, Image: "pages/p0.png", DurationMs: 5000}}
	if isGazeLookPage(withShot) {
		t.Fatal("shots stay on Chrome")
	}
	withSlide := base
	withSlide.Transition = Transition{Type: animation.TransitionSlide, DurationMs: 240}
	if isGazeLookPage(withSlide) {
		t.Fatal("slide transition stays on Chrome")
	}
	withBob := base
	withBob.Layers = []Layer{{ElementID: "a", Clips: []Motion{{Effect: "bob", DurationMs: 2000}}}}
	if isGazeLookPage(withBob) {
		t.Fatal("non-entrance clips stay on Chrome")
	}
}

func TestCollectLookJobsFiveLooks(t *testing.T) {
	plan := &Timeline{
		FPS: 30, Width: 1920, Height: 1080,
		Pages: []Page{{
			Index: 0, DurationMs: 80000, BaseImage: "pages/p0.png",
			Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
			Focus: []FocusSpan{
				{StartMs: 0, ElementID: "a", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}},
				{StartMs: 16000, ElementID: "b", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}},
				{StartMs: 32000, ElementID: "c", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}},
				{StartMs: 48000, ElementID: "d", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}},
				{StartMs: 64000, ElementID: "e", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}},
			},
		}},
	}
	jobs, err := collectLookJobs(plan, "job", []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 5 {
		t.Fatalf("jobs = %d, want 5", len(jobs))
	}
	for i, j := range jobs {
		if j.Frame < 0 || j.Out == "" {
			t.Fatalf("job[%d] = %+v", i, j)
		}
		if !strings.Contains(j.Out, "p000_l0") {
			t.Fatalf("job[%d].out = %q", i, j.Out)
		}
	}
	if jobs[0].Frame == jobs[4].Frame {
		t.Fatal("first and last looks must sample different frames")
	}
}

func TestConcatListBodyUsesFFmpegSingleQuotes(t *testing.T) {
	dir := t.TempDir()
	clips := []string{
		filepath.Join(dir, "looks", "p000_l00.mp4"),
		filepath.Join(dir, "looks", "p000_l01.mp4"),
	}
	body := concatListBody(dir, clips)
	if strings.Contains(body, `\"`) {
		t.Fatalf("Go strconv.Quote leaked into concat list:\n%s", body)
	}
	want := "file 'looks/p000_l00.mp4'\nfile 'looks/p000_l01.mp4'\n"
	if body != want {
		t.Fatalf("concat list =\n%q\nwant\n%q", body, want)
	}
}

func TestLookFrameCountsSumsToPage(t *testing.T) {
	const pageMs int64 = 83840
	const fps = 30
	const n = 12
	windows := make([]lookWindow, n)
	for i := 0; i < n; i++ {
		windows[i] = lookWindow{
			StartMs: pageMs * int64(i) / int64(n),
			EndMs:   pageMs * int64(i+1) / int64(n),
		}
	}
	pageFrames := framesForDuration(pageMs, fps)
	indep := 0
	for _, w := range windows {
		indep += framesForDuration(w.EndMs-w.StartMs, fps)
	}
	if indep == pageFrames {
		t.Fatalf("fixture must drift under independent rounding: indep=%d page=%d", indep, pageFrames)
	}
	got := lookFrameCounts(windows, pageFrames, fps)
	if len(got) != n {
		t.Fatalf("counts = %d, want %d", len(got), n)
	}
	sum := 0
	for i, f := range got {
		if f < 1 {
			t.Fatalf("look[%d] frames = %d", i, f)
		}
		sum += f
	}
	if sum != pageFrames {
		t.Fatalf("sum = %d, want page frames %d (independent was %d)", sum, pageFrames, indep)
	}
}

func TestEncodeGazePageArgsOneLookHold(t *testing.T) {
	args, err := encodeGazePageArgs(gazePageEncodeSpec{
		Width: 1920, Height: 1080, FPS: 30, CRF: 18, GOP: 30,
		Background: "#FFFFFF", Out: "segment_000.mp4", PageFrames: 480,
		Looks: []gazeLookEncode{{Image: "looks/p000_l00.png", Frames: 480}},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-i\nlooks/p000_l00.png",
		"trim=end_frame=480",
		"-frames:v\n480",
		"-preset\nultrafast",
		"-crf\n18",
		"-c:v\nlibx264",
		"segment_000.mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "concat=n=") {
		t.Fatalf("one look must not concat:\n%s", joined)
	}
	if strings.Count(joined, "-c:v") != 1 {
		t.Fatalf("want one encode, got:\n%s", joined)
	}
}

func TestEncodeGazePageArgsXfadeConcatOnce(t *testing.T) {
	args, err := encodeGazePageArgs(gazePageEncodeSpec{
		Width: 1920, Height: 1080, FPS: 30, CRF: 18, GOP: 30,
		Background: "#FFFFFF", Out: "segment_001.mp4", PageFrames: 960,
		Looks: []gazeLookEncode{
			{Image: "looks/p001_l00.png", Frames: 480},
			{Image: "looks/p001_l01.png", PrevImage: "looks/p001_l00.png", Frames: 480, TransitionFrames: 7},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-i\nlooks/p001_l00.png",
		"-i\nlooks/p001_l01.png",
		"concat=n=2:v=1:a=0",
		`expr='A*pow(1-P\,3)+B*(1-pow(1-P\,3))'`,
		"-frames:v\n960",
		"-preset\nultrafast",
		"tpad=stop_mode=clone:stop_duration=1",
		"-colorspace\nbt709",
		"segment_001.mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-f\nconcat") {
		t.Fatal("page graph must not use the concat demuxer")
	}
	if strings.Contains(joined, "-preset\nmedium") {
		t.Fatal("look graph uses ultrafast")
	}
	if strings.Count(joined, "-c:v") != 1 {
		t.Fatalf("want one encode, got:\n%s", joined)
	}
}

func TestEncodeGazePageArgsPageHeadFade(t *testing.T) {
	args, err := encodeGazePageArgs(gazePageEncodeSpec{
		Width: 1920, Height: 1080, FPS: 30, Out: "segment_002.mp4", PageFrames: 90,
		Looks: []gazeLookEncode{{
			Image: "looks/p002_l00.png", PrevImage: "looks/p001_l05.png",
			Frames: 90, TransitionFrames: 7,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "-i\nlooks/p001_l05.png") {
		t.Fatalf("missing previous page still:\n%s", joined)
	}
	if !strings.Contains(joined, "-i\nlooks/p002_l00.png") {
		t.Fatalf("missing look still:\n%s", joined)
	}
	if !strings.Contains(joined, "xfade=transition=custom:duration=0.233:offset=0") {
		t.Fatalf("missing page-head xfade:\n%s", joined)
	}
}

func TestEncodeGazePageArgsRejectsOddCanvas(t *testing.T) {
	if _, err := encodeGazePageArgs(gazePageEncodeSpec{
		Width: 1919, Height: 1080, Out: "o.mp4", PageFrames: 1,
		Looks: []gazeLookEncode{{Image: "a.png", Frames: 1}},
	}); err == nil {
		t.Fatal("odd width must be rejected")
	}
}

func TestConcatLooksArgsFitsExactFrames(t *testing.T) {
	args := concatLooksArgs("page.concat.txt", "segment_001.mp4", 2515, 30, 18, 30)
	joined := strings.Join(args, "\n")
	for _, want := range []string{
		"-f\nconcat",
		"tpad=stop_mode=clone:stop_duration=1",
		"-frames:v\n2515",
		"-c:v\nlibx264",
		"segment_001.mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "-c\ncopy") {
		t.Fatal("look concat must re-encode to pin duration, not copy")
	}
}

func TestConcatListBodyEscapesSingleQuote(t *testing.T) {
	dir := t.TempDir()
	got := concatListBody(dir, []string{filepath.Join(dir, "it's.mp4")})
	want := "file 'it'\\''s.mp4'\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStillPrevImagePrefersLastLookStill(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "looks"), 0o750); err != nil {
		t.Fatal(err)
	}
	last := lookStillPath(dir, 1, 1)
	if err := os.WriteFile(last, []byte("png"), 0o640); err != nil {
		t.Fatal(err)
	}
	plan := &Timeline{
		FPS: 30,
		Pages: []Page{
			{Index: 0, DurationMs: 1000, BaseImage: "pages/p0.png"},
			{Index: 1, DurationMs: 4000, BaseImage: "pages/p1.png", Focus: []FocusSpan{
				{StartMs: 0, Rect: &Box{X: 1, Y: 1, W: 8, H: 8}},
				{StartMs: 2000, Rect: &Box{X: 1, Y: 1, W: 8, H: 8}},
			}},
			{Index: 2, DurationMs: 3000, BaseImage: "pages/p2.png", Transition: Transition{Type: animation.TransitionFade, DurationMs: 240}},
		},
	}
	got, err := stillPrevImage(context.Background(), "ffmpeg-unused", plan, dir, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != last {
		t.Fatalf("prev = %q, want last look still %q", got, last)
	}
}

func TestExtractedFrameOKRejectsMissing(t *testing.T) {
	if err := extractedFrameOK(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Fatal("missing extract output must be an error")
	}
}
