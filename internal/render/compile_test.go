package render

import (
	"errors"
	"fmt"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

const (
	emuWidescreen = 12192000
	emuStandard   = 9144000
	emuHeight     = 6858000
	// EMU/px = 6350（1920 宽画布）——本包里所有几何断言都用这个换算核对。
	emuPerPixel = 6350
)

func ptr[T any](v T) *T { return &v }

func newTestCompiler(t *testing.T) *Compiler {
	t.Helper()
	c, err := NewCompiler(Options{
		Canvas:         design.Canvas{Width: 1920, Height: 1080},
		SlideWidthEMU:  emuWidescreen,
		SlideHeightEMU: emuHeight,
		FPS:            30,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func emuBox(x, y, w, h int64) *EMUBox {
	return &EMUBox{X: x * emuPerPixel, Y: y * emuPerPixel,
		Width: w * emuPerPixel, Height: h * emuPerPixel}
}

func clipOf(id, effect string, start, dur int64, trigger string) *animation.Clip {
	return &animation.Clip{ID: id, Type: effect, StartMs: start, DurationMs: dur,
		Easing: "easeOut", Trigger: trigger}
}

func revisionOf(preset string, intensity float64, tracks ...*animation.Track) *animation.Revision {
	return &animation.Revision{PresetID: preset, Intensity: intensity, Tracks: tracks}
}

func TestGeometryEMUToPixels(t *testing.T) {
	c := newTestCompiler(t)
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "slide-1", DurationMs: 6000, BaseImage: "pages/p0.png",
		Elements: []Element{{ID: "shape-1", Kind: animation.KindTitle, Bounds: emuBox(96, 54, 960, 540)}},
		Revision: revisionOf("business-calm", 1, &animation.Track{
			ElementID: "shape-1", Kind: animation.KindTitle,
			Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 500, "onEnter")},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(page.Layers))
	}
	want := Box{X: 96, Y: 54, W: 960, H: 540}
	if page.Layers[0].Box != want {
		t.Fatalf("box = %+v, want %+v", page.Layers[0].Box, want)
	}
	for _, d := range diags {
		if d.Code == DiagGeometryInferred {
			t.Fatal("显式提供页尺寸时不应出现几何推断诊断")
		}
	}
}

func TestGeometryStandard43Canvas(t *testing.T) {
	c, err := NewCompiler(Options{
		Canvas:        design.Canvas{Width: 1440, Height: 1080},
		SlideWidthEMU: emuStandard, SlideHeightEMU: emuHeight,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 4000, BaseImage: "p0.png",
		Elements: []Element{{ID: "e", Kind: animation.KindShape, Bounds: emuBox(72, 72, 600, 300)}},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindShape,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 400, "onEnter")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := page.Layers[0].Box, (Box{X: 72, Y: 72, W: 600, H: 300}); got != want {
		t.Fatalf("box = %+v, want %+v", got, want)
	}
}

func TestGeometryAspectMismatchRejected(t *testing.T) {
	// 16:9 的页尺寸 + 4:3 的画布：等比缩放后盒子必然错位，宁可拒绝。
	_, err := NewCompiler(Options{
		Canvas:        design.Canvas{Width: 1440, Height: 1080},
		SlideWidthEMU: emuWidescreen, SlideHeightEMU: emuHeight,
	})
	if !errors.Is(err, ErrAspectMismatch) {
		t.Fatalf("err = %v, want ErrAspectMismatch", err)
	}
}

func TestGeometryInferredWhenUnknown(t *testing.T) {
	c, err := NewCompiler(Options{Canvas: design.Canvas{Width: 1920, Height: 1080}})
	if err != nil {
		t.Fatal(err)
	}
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 3000, BaseImage: "p0.png",
		Elements: []Element{{ID: "e", Kind: animation.KindShape, Bounds: emuBox(0, 0, 1920, 1080)}},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindShape,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 400, "onEnter")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 推断出的页宽 12192000 EMU，与 16:9 一致：整页盒应铺满画布。
	if got := page.Layers[0].Box; got != (Box{X: 0, Y: 0, W: 1920, H: 1080}) {
		t.Fatalf("box = %+v", got)
	}
	if !hasDiag(diags, DiagGeometryInferred) {
		t.Fatal("推断页尺寸时必须留诊断")
	}
}

func TestUnknownDurationFails(t *testing.T) {
	c := newTestCompiler(t)
	_, _, err := c.CompilePage(PageInput{Index: 0, SlideID: "s", DurationMs: 0, BaseImage: "p0.png"})
	if !errors.Is(err, ErrDurationUnknown) {
		t.Fatalf("err = %v, want ErrDurationUnknown", err)
	}
}

func TestSegmentTriggerResolvesToPageOffset(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 1, SlideID: "s2", DurationMs: 6400, BaseImage: "p1.png",
		Elements:       []Element{{ID: "e", Kind: animation.KindBullet, Bounds: emuBox(100, 100, 400, 100)}},
		SegmentOffsets: map[string]int64{"seg-3": 3500},
		Revision: revisionOf("business-calm", 1, &animation.Track{ElementID: "e", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 500, "withSegment:seg-3")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Layers[0].Clips[0].StartMs; got != 3500 {
		t.Fatalf("startMs = %d, want 3500", got)
	}
}

func TestSegmentUnresolvedFallsBackToZero(t *testing.T) {
	c := newTestCompiler(t)
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 4000, BaseImage: "p0.png",
		Elements:       []Element{{ID: "e", Kind: animation.KindBullet, Bounds: emuBox(10, 10, 300, 80)}},
		SegmentOffsets: map[string]int64{},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 400, "withSegment:seg-9")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 关键：锚定失效不能丢整页，也不能让导出失败。
	if len(page.Layers) != 1 || len(page.Layers[0].Clips) != 1 {
		t.Fatalf("锚定失效后仍应保留 clip，实际 layers=%d", len(page.Layers))
	}
	if page.Layers[0].Clips[0].StartMs != 0 {
		t.Fatalf("startMs = %d, want 0", page.Layers[0].Clips[0].StartMs)
	}
	if !hasDiag(diags, DiagSegmentUnresolved) {
		t.Fatal("必须留下 segment_unresolved 诊断")
	}
}

func TestMissingAnchorSkipsTrackOnly(t *testing.T) {
	c := newTestCompiler(t)
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 4000, BaseImage: "p0.png",
		Elements: []Element{{ID: "keep", Kind: animation.KindTitle, Bounds: emuBox(100, 100, 800, 200)}},
		Revision: revisionOf("", 1,
			&animation.Track{ElementID: "gone", Kind: animation.KindTitle,
				Clips: []*animation.Clip{clipOf("c1", "fadeIn", 0, 400, "onEnter")}},
			&animation.Track{ElementID: "keep", Kind: animation.KindTitle,
				Clips: []*animation.Clip{clipOf("c2", "fadeIn", 0, 400, "onEnter")}},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 1 || page.Layers[0].ElementID != "keep" {
		t.Fatalf("应只保留存在的锚点，实际 %+v", page.Layers)
	}
	if !hasDiag(diags, DiagAnchorMissing) {
		t.Fatal("缺失锚点必须留诊断")
	}
}

func TestBoxClampedToCanvas(t *testing.T) {
	c := newTestCompiler(t)
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 4000, BaseImage: "p0.png",
		// 右边界超出 40px、下边界超出 20px。
		Elements: []Element{{ID: "e", Kind: animation.KindShape,
			Bounds: &EMUBox{X: 1800 * emuPerPixel, Y: 1000 * emuPerPixel,
				Width: 160 * emuPerPixel, Height: 100 * emuPerPixel}}},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindShape,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 400, "onEnter")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	box := page.Layers[0].Box
	if box.X+box.W != 1920 || box.Y+box.H != 1080 {
		t.Fatalf("box 未被钳制到画布内: %+v", box)
	}
	if !hasDiag(diags, DiagBoxClamped) {
		t.Fatal("钳制必须留诊断")
	}
}

func TestNoBoundsSkipsTrack(t *testing.T) {
	c := newTestCompiler(t)
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 4000, BaseImage: "p0.png",
		Elements: []Element{{ID: "e", Kind: animation.KindShape}},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindShape,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 400, "onEnter")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 0 {
		t.Fatalf("无几何元素不应产生图层: %+v", page.Layers)
	}
	if !hasDiag(diags, DiagAnchorMissing) {
		t.Fatal("无几何必须留诊断")
	}
}

// TestIntensityChangesAmplitudeOnly 固定 D2：强度只缩放幅度，不改时长。
func TestIntensityChangesAmplitudeOnly(t *testing.T) {
	c := newTestCompiler(t)
	mk := func(intensity float64) Motion {
		page, _, err := c.CompilePage(PageInput{
			Index: 0, SlideID: "s", DurationMs: 6000, BaseImage: "p0.png",
			Elements: []Element{{ID: "e", Kind: animation.KindTitle, Bounds: emuBox(100, 100, 800, 200)}},
			Revision: revisionOf("business-calm", intensity, &animation.Track{ElementID: "e",
				Kind: animation.KindTitle,
				Clips: []*animation.Clip{{ID: "c", Type: "slideIn", DurationMs: 600,
					Easing: "easeOut", Params: animation.Params{Direction: "up", Offset: 40},
					Trigger: "onEnter"}}}),
		})
		if err != nil {
			t.Fatal(err)
		}
		return page.Layers[0].Clips[0]
	}
	base := mk(1.0)
	loud := mk(1.5)
	if base.DurationMs != 600 || loud.DurationMs != 600 {
		t.Fatalf("时长不应随强度变化: %d vs %d", base.DurationMs, loud.DurationMs)
	}
	if got := propFrom(loud, animation.PropTranslateY); got != 60 {
		t.Fatalf("translateY.from = %v, want 60", got)
	}
	if got := propFrom(base, animation.PropTranslateY); got != 40 {
		t.Fatalf("translateY.from = %v, want 40", got)
	}
}

func TestClipTruncatedAtPageEnd(t *testing.T) {
	c := newTestCompiler(t)
	page, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 1000, BaseImage: "p0.png",
		Elements: []Element{{ID: "e", Kind: animation.KindTitle, Bounds: emuBox(0, 0, 800, 200)}},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindTitle,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 800, 900, "atTime:800")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Layers[0].Clips[0].DurationMs; got != 200 {
		t.Fatalf("duration = %d, want 200", got)
	}
	if !hasDiag(diags, DiagClipTruncated) {
		t.Fatal("截断必须留诊断")
	}
}

func TestTransitions(t *testing.T) {
	c := newTestCompiler(t)
	page := func(idx int, preset string) Page {
		p, _, err := c.CompilePage(PageInput{
			Index: idx, SlideID: "s", DurationMs: 3000, BaseImage: "p.png",
			Revision: revisionOf(preset, 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if tr := page(0, "tech-dynamic").Transition; tr.Type != animation.TransitionNone || tr.DurationMs != 0 {
		t.Fatalf("首屏转场必须为 none，实际 %+v", tr)
	}
	if tr := page(1, "tech-dynamic").Transition; tr.Type != animation.TransitionSlide || tr.DurationMs != 280 {
		t.Fatalf("第二页转场应取预设 tech-dynamic，实际 %+v", tr)
	}
	if tr := page(1, "").Transition; tr.Type != animation.TransitionFade || tr.DurationMs != 240 {
		t.Fatalf("自定义预设应回退 fade/240，实际 %+v", tr)
	}
	// 不存在的预设：回退 + 诊断。
	p, diags, err := c.CompilePage(PageInput{
		Index: 1, SlideID: "s", DurationMs: 3000, BaseImage: "p.png",
		Revision: revisionOf("nope", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Transition.Type != animation.TransitionFade {
		t.Fatalf("未知预设应回退 fade，实际 %+v", p.Transition)
	}
	if !hasDiag(diags, DiagTransitionDefaulted) {
		t.Fatal("未知预设必须留诊断")
	}
}

func TestPageLevelTrackCoversCanvas(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 3000, BaseImage: "p.png",
		Revision: revisionOf("", 1, &animation.Track{ElementID: "", Kind: animation.KindPage,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 400, "onEnter")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Layers) != 1 || page.Layers[0].AssetMode != AssetPageLayer {
		t.Fatalf("整页轨道应以 page-layer 形式保留: %+v", page.Layers)
	}
	if got := page.Layers[0].Box; got != (Box{X: 0, Y: 0, W: 1920, H: 1080}) {
		t.Fatalf("整页盒 = %+v", got)
	}
}

func TestTierConflictIsAggregatedPerPage(t *testing.T) {
	c := newTestCompiler(t)
	_, diags, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 8000, BaseImage: "p.png",
		Elements: []Element{{ID: "e", Kind: animation.KindIcon, Bounds: emuBox(10, 10, 60, 60)}},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindIcon,
			// 小元素档 240–400ms，2000ms 的 bob 必然不在档内。
			Clips: []*animation.Clip{{ID: "c", Type: "bob", DurationMs: 2000, Easing: "linear",
				Params: animation.Params{Loop: true}, Trigger: "onEnter"}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range diags {
		if d.Code == DiagDurationTierConflict {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("分层冲突应按页汇总为 1 条，实际 %d 条", n)
	}
	// 冲突只诊断，不改时长、不出片失败。
	if len(diags) > 0 {
		for _, d := range diags {
			if d.Code == "plan_motion_failed" {
				t.Fatalf("不应因分层冲突失败: %s", d.Message)
			}
		}
	}
}

// TestPresetDurationsDoNotSpamTierWarnings 固定容差的存在理由：
// catalog 预设（标题 700ms、要点 500ms）与视觉基线的速度分层本就不同源，
// 若按严格区间判定，几乎每一页都会带警告，真正的异常会被淹没。
func TestPresetDurationsDoNotSpamTierWarnings(t *testing.T) {
	c := newTestCompiler(t)
	for _, preset := range []string{"business-calm", "tech-dynamic", "teaching-clear", "launch-bold"} {
		t.Run(preset, func(t *testing.T) {
			_, diags, err := c.CompilePage(PageInput{
				Index: 0, SlideID: "s", DurationMs: 8000, BaseImage: "p.png",
				Elements: []Element{
					{ID: "t", Kind: animation.KindTitle, Bounds: emuBox(96, 72, 1440, 260)},
					{ID: "b", Kind: animation.KindBullet, Bounds: emuBox(120, 400, 820, 420)},
				},
				Revision: revisionOf(preset, 1,
					&animation.Track{ElementID: "t", Kind: animation.KindTitle,
						Clips: presetClip(t, preset, animation.KindTitle)},
					&animation.Track{ElementID: "b", Kind: animation.KindBullet,
						Clips: presetClip(t, preset, animation.KindBullet)}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if hasDiag(diags, DiagDurationTierConflict) {
				t.Fatalf("预设 %s 不应触发分层警告: %+v", preset, diags)
			}
		})
	}
}

// presetClip 把预设里的模板转成 clip（默认参数，仅取时长/缓动）。
func presetClip(t *testing.T, presetID, kind string) []*animation.Clip {
	t.Helper()
	p, ok := animation.Catalog().Preset(presetID)
	if !ok {
		t.Fatalf("预设 %q 不存在", presetID)
	}
	tpls := p.Clips[kind]
	if len(tpls) == 0 {
		return nil
	}
	out := make([]*animation.Clip, 0, len(tpls))
	for i, tpl := range tpls {
		out = append(out, &animation.Clip{
			ID: fmt.Sprintf("c%d", i+1), Type: tpl.Type, DurationMs: tpl.DurationMs,
			Easing: tpl.Easing, Params: tpl.Params, Trigger: "onEnter",
		})
	}
	return out
}

func TestCompileProducesValidPlan(t *testing.T) {
	c := newTestCompiler(t)
	plan, err := c.Compile([]PageInput{
		{Index: 0, SlideID: "s1", DurationMs: 6000, BaseImage: "pages/p0.png",
			Elements: []Element{{ID: "t", Kind: animation.KindTitle, Bounds: emuBox(96, 72, 1200, 200)}},
			Revision: revisionOf("business-calm", 1, &animation.Track{ElementID: "t",
				Kind:  animation.KindTitle,
				Clips: []*animation.Clip{clipOf("c1", "slideIn", 0, 700, "onEnter")}})},
		{Index: 1, SlideID: "s2", DurationMs: 5000, BaseImage: "pages/p1.png",
			Revision: revisionOf("business-calm", 1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("编译产物必须自洽: %v", err)
	}
	if len(plan.Pages) != 2 || plan.Pages[1].Transition.Type != animation.TransitionFade {
		t.Fatalf("页数/转场不符: %+v", plan.Pages)
	}
	if plan.TotalDurationMs() != 11000 {
		t.Fatalf("总时长 = %d", plan.TotalDurationMs())
	}
}

func hasDiag(ds []Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}

func propFrom(m Motion, name string) float64 {
	for _, p := range m.Props {
		if p.Name == name {
			return p.From
		}
	}
	return -1
}
