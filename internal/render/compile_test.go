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

// clip.startMs 是相对锚点的偏移：onEnter 的错落出场必须各自保留起点，
// 否则编排里的 700/920/1140ms 会全部塌缩到 0ms（三条同时淡入）。
func TestOnEnterClipKeepsRelativeOffset(t *testing.T) {
	c := newTestCompiler(t)
	rev := revisionOf("business-calm", 1,
		&animation.Track{ElementID: "e0", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c0", "fadeIn", 0, 400, "onEnter")}},
		&animation.Track{ElementID: "e1", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c1", "fadeIn", 700, 400, "onEnter")}},
		&animation.Track{ElementID: "e2", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c2", "fadeIn", 920, 400, "onEnter")}},
	)
	page, _, err := c.CompilePage(PageInput{
		Index: 0, SlideID: "s", DurationMs: 6000, BaseImage: "p0.png",
		Elements: []Element{
			{ID: "e0", Kind: animation.KindBullet, Bounds: emuBox(100, 100, 400, 80)},
			{ID: "e1", Kind: animation.KindBullet, Bounds: emuBox(100, 200, 400, 80)},
			{ID: "e2", Kind: animation.KindBullet, Bounds: emuBox(100, 300, 400, 80)},
		},
		Revision: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"e0": 0, "e1": 700, "e2": 920}
	for _, layer := range page.Layers {
		if len(layer.Clips) != 1 {
			t.Fatalf("%s clips = %d, want 1", layer.ElementID, len(layer.Clips))
		}
		if got := layer.Clips[0].StartMs; got != want[layer.ElementID] {
			t.Fatalf("%s startMs = %d, want %d", layer.ElementID, got, want[layer.ElementID])
		}
	}
}

// 锚点与偏移相加：withSegment 给出锚点，startMs 在其上再偏移。
func TestSegmentTriggerAddsClipOffset(t *testing.T) {
	c := newTestCompiler(t)
	page, _, err := c.CompilePage(PageInput{
		Index: 1, SlideID: "s2", DurationMs: 6400, BaseImage: "p1.png",
		Elements:       []Element{{ID: "e", Kind: animation.KindBullet, Bounds: emuBox(100, 100, 400, 100)}},
		SegmentOffsets: map[string]int64{"seg-3": 3500},
		Revision: revisionOf("", 1, &animation.Track{ElementID: "e", Kind: animation.KindBullet,
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 120, 500, "withSegment:seg-3")}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Layers[0].Clips[0].StartMs; got != 3620 {
		t.Fatalf("startMs = %d, want 3620 (3500+120)", got)
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
			// atTime 已给出绝对锚点，startMs 只作相对偏移（这里取 0）。
			// 旧写法把 800ms 同时写进两处，靠「startMs 被忽略」才通过——那正是被修掉的缺陷。
			Clips: []*animation.Clip{clipOf("c", "fadeIn", 0, 900, "atTime:800")}}),
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

// 部分重叠（互不包含）的兄弟元素是 ADR-9 抹除算法管不到的情形：两张层位图会各自
// 烘进对方落在重叠区的像素。修不掉（单张栅格推不出逐元素 alpha），但不能静默——
// 编译期给一条诊断，运维/调用方才能知道这一页为什么「动一个另一个跟着动」。
func TestCompilePageReportsPartialSiblingOverlap(t *testing.T) {
	c := newTestCompiler(t)
	compile := func(t *testing.T, boxes map[string]*EMUBox) []Diagnostic {
		t.Helper()
		elems := make([]Element, 0, len(boxes))
		var tracks []*animation.Track
		i := 0
		for id, b := range boxes {
			elems = append(elems, Element{ID: id, Kind: animation.KindBullet, Bounds: b})
			tracks = append(tracks, &animation.Track{ElementID: id, Kind: animation.KindBullet,
				Clips: []*animation.Clip{clipOf(id+"-in", "fadeIn", int64(i*200), 500, "onEnter")}})
			i++
		}
		_, diags, err := c.CompilePage(PageInput{
			Index: 1, SlideID: "slide-2", DurationMs: 6000, BaseImage: "pages/p1.png",
			Elements: elems, Revision: revisionOf("business-calm", 1, tracks...),
		})
		if err != nil {
			t.Fatal(err)
		}
		return diags
	}

	t.Run("部分重叠要报", func(t *testing.T) {
		diags := compile(t, map[string]*EMUBox{
			"card": emuBox(0, 0, 400, 300),
			"note": emuBox(300, 150, 400, 300),
		})
		if !hasDiag(diags, DiagSiblingOverlapPartial) {
			t.Fatalf("缺 %s 诊断，got %+v", DiagSiblingOverlapPartial, diags)
		}
	})

	t.Run("包含关系不报（抹除算法已覆盖）", func(t *testing.T) {
		diags := compile(t, map[string]*EMUBox{
			"card": emuBox(0, 0, 600, 600),
			"text": emuBox(100, 100, 300, 100),
		})
		if hasDiag(diags, DiagSiblingOverlapPartial) {
			t.Fatalf("包含关系不该报 %s，got %+v", DiagSiblingOverlapPartial, diags)
		}
	})

	t.Run("不相交不报", func(t *testing.T) {
		diags := compile(t, map[string]*EMUBox{
			"left":  emuBox(0, 0, 300, 300),
			"right": emuBox(800, 0, 300, 300),
		})
		if hasDiag(diags, DiagSiblingOverlapPartial) {
			t.Fatalf("不相交不该报 %s，got %+v", DiagSiblingOverlapPartial, diags)
		}
	})
}

// ---- G8-② 跨页延续编译拆分测试 ----

func findLayerByElementID(layers []Layer, elementID string) *Layer {
	for i := range layers {
		if layers[i].ElementID == elementID {
			return &layers[i]
		}
	}
	return nil
}

func findMotionByContinuation(ms []Motion, cont bool) *Motion {
	for i := range ms {
		if ms[i].IsContinuation == cont {
			return &ms[i]
		}
	}
	return nil
}

// makeContinuationPages 构造相邻两页，shape-1 在两页都存在；p0clip 为页 0 的待测 clip。
func makeContinuationPages(p0clip *animation.Clip) []PageInput {
	return []PageInput{
		{Index: 0, SlideID: "s1", DurationMs: 6000, BaseImage: "pages/p0.png",
			Elements: []Element{{ID: "shape-1", Kind: animation.KindTitle, Bounds: emuBox(96, 54, 960, 540)}},
			Revision: revisionOf("business-calm", 1, &animation.Track{
				ElementID: "shape-1", Kind: animation.KindTitle, Clips: []*animation.Clip{p0clip},
			})},
		{Index: 1, SlideID: "s2", DurationMs: 5000, BaseImage: "pages/p1.png",
			Elements: []Element{{ID: "shape-1", Kind: animation.KindTitle, Bounds: emuBox(96, 54, 960, 540)}},
			Revision: revisionOf("business-calm", 1, &animation.Track{
				ElementID: "shape-1", Kind: animation.KindTitle,
				Clips:     []*animation.Clip{clipOf("c2", "slideIn", 0, 500, "onEnter")},
			})},
	}
}

func TestCompileContinuationSplitsAcrossPages(t *testing.T) {
	c := newTestCompiler(t)
	// 页 0 时长 6000，clip 从 0 起、时长 8000 → 溢出 2000 到页 1（offset = 6000）。
	pages := makeContinuationPages(&animation.Clip{
		ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 8000,
		Easing: "easeOut", Trigger: "onEnter", Continues: true,
	})
	plan, err := c.Compile(pages)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pages) != 2 {
		t.Fatalf("页数 = %d", len(plan.Pages))
	}

	p0 := findLayerByElementID(plan.Pages[0].Layers, "shape-1")
	if p0 == nil || len(p0.Clips) != 1 {
		t.Fatalf("页 0 缺 shape-1 层或段数不对: %+v", p0)
	}
	n0 := p0.Clips[0]
	if n0.IsContinuation {
		t.Fatalf("页 0 段不应是 continuation")
	}
	// 本页段保留完整 clip：起点 0、时长=原长 8000（不截断到页尾）。
	if n0.StartMs != 0 || n0.DurationMs != 8000 {
		t.Fatalf("页 0 段 start/dur = %d/%d，want 0/8000", n0.StartMs, n0.DurationMs)
	}
	// 本页段是「源」，不携带跨页身份键（仅延续段携带）。
	if n0.ElementID != "" {
		t.Fatalf("页 0 段不应携带 ElementID，got %q", n0.ElementID)
	}

	p1 := findLayerByElementID(plan.Pages[1].Layers, "shape-1")
	if p1 == nil {
		t.Fatalf("页 1 缺 shape-1 层")
	}
	cont := findMotionByContinuation(p1.Clips, true)
	if cont == nil {
		t.Fatalf("页 1 缺 continuation 段，clips=%d", len(p1.Clips))
	}
	// 次页段用负起点把求值时间平移回原 clip 时间轴：offset=6000 → StartMs=-6000，
	// DurationMs=原长 8000。于是页 1 localMs=0 时原 clip 进度 = (0+6000)/8000 = 0.75，
	// 与页 0 末态（页 0 t=6000 时 (6000-0)/8000=0.75）完全一致 → 页缝视觉连续。
	if cont.StartMs != -6000 || cont.DurationMs != 8000 {
		t.Fatalf("页 1 段 start/dur = %d/%d，want -6000/8000", cont.StartMs, cont.DurationMs)
	}
	if cont.ContinuationOffsetMs != 6000 {
		t.Fatalf("ContinuationOffsetMs = %d，want 6000", cont.ContinuationOffsetMs)
	}
	if cont.ElementID != "shape-1" {
		t.Fatalf("页 1 段 ElementID = %q", cont.ElementID)
	}

	// 视觉连续：两段属性完全一致（都是完整 from→to），仅起点平移。
	if len(n0.Props) != len(cont.Props) {
		t.Fatalf("两段 Props 数不等: %d vs %d", len(n0.Props), len(cont.Props))
	}
	for i := range n0.Props {
		if n0.Props[i].Name != cont.Props[i].Name ||
			n0.Props[i].From != cont.Props[i].From ||
			n0.Props[i].To != cont.Props[i].To {
			t.Fatalf("两段 Props 不一致: 页0 %+v vs 页1 %+v", n0.Props[i], cont.Props[i])
		}
	}
	// 数学不变量：次页段起点 = -offset，且两段时间轴同源（同 DurationMs）。
	if cont.StartMs != -cont.ContinuationOffsetMs {
		t.Fatalf("次页段起点 %d != -ContinuationOffsetMs(%d)，页缝不连续", cont.StartMs, cont.ContinuationOffsetMs)
	}
	if n0.DurationMs != cont.DurationMs {
		t.Fatalf("两段时长不一致: %d vs %d", n0.DurationMs, cont.DurationMs)
	}
}

func TestCompileContinuationNonContinuesTruncates(t *testing.T) {
	c := newTestCompiler(t)
	pages := makeContinuationPages(&animation.Clip{
		ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 8000,
		Easing: "easeOut", Trigger: "onEnter", // 无 Continues
	})
	plan, err := c.Compile(pages)
	if err != nil {
		t.Fatal(err)
	}
	foundTrunc := false
	for _, d := range plan.Warnings {
		if d.Code == DiagClipTruncated {
			foundTrunc = true
		}
	}
	if !foundTrunc {
		t.Fatalf("非延续溢出必须记 clip_truncated，got %+v", plan.Warnings)
	}
	// 页 1 不应有 continuation 段
	p1 := findLayerByElementID(plan.Pages[1].Layers, "shape-1")
	if p1 != nil {
		for _, m := range p1.Clips {
			if m.IsContinuation {
				t.Fatalf("非延续 clip 不应产生 continuation 段")
			}
		}
	}
	// 页 0 段必须是完整 from→to（截断而非拆分），且不应携带 ElementID
	p0 := findLayerByElementID(plan.Pages[0].Layers, "shape-1")
	if p0 == nil || len(p0.Clips) != 1 {
		t.Fatalf("页 0 段异常: %+v", p0)
	}
	if p0.Clips[0].ElementID != "" {
		t.Fatalf("非延续段不应带 ElementID，got %q", p0.Clips[0].ElementID)
	}
}

func TestCompileContinuationMissingTarget(t *testing.T) {
	c := newTestCompiler(t)
	// 页 1 存在，但 shape-1 在页 1 无对应层（页 1 只有另一个元素 other）。
	pages := []PageInput{
		{Index: 0, SlideID: "s1", DurationMs: 6000, BaseImage: "pages/p0.png",
			Elements: []Element{{ID: "shape-1", Kind: animation.KindTitle, Bounds: emuBox(96, 54, 960, 540)}},
			Revision: revisionOf("business-calm", 1, &animation.Track{
				ElementID: "shape-1", Kind: animation.KindTitle,
				Clips: []*animation.Clip{{ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 8000,
					Easing: "easeOut", Trigger: "onEnter", Continues: true}},
			})},
		{Index: 1, SlideID: "s2", DurationMs: 5000, BaseImage: "pages/p1.png",
			Elements: []Element{{ID: "other", Kind: animation.KindTitle, Bounds: emuBox(96, 54, 960, 540)}},
			Revision: revisionOf("business-calm", 1, &animation.Track{
				ElementID: "other", Kind: animation.KindTitle,
				Clips: []*animation.Clip{{ID: "c2", Type: "slideIn", StartMs: 0, DurationMs: 500,
					Easing: "easeOut", Trigger: "onEnter"}},
			})},
	}
	plan, err := c.Compile(pages)
	if err != nil {
		t.Fatal(err)
	}
	foundMissing := false
	for _, d := range plan.Warnings {
		if d.Code == DiagContinuationTargetMissing {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("次页无承接元素必须记 continuation_target_missing，got %+v", plan.Warnings)
	}
	// 不应向页 1 注入任何 continuation 段
	p1other := findLayerByElementID(plan.Pages[1].Layers, "other")
	if p1other != nil {
		for _, m := range p1other.Clips {
			if m.IsContinuation {
				t.Fatalf("次页缺失目标时不应注入 continuation 段")
			}
		}
	}
}

func TestCompileContinuationLastPage(t *testing.T) {
	c := newTestCompiler(t)
	// 只有一页（最后一页），continues 溢出 → 必须记 continuation_target_missing 且不崩。
	pages := []PageInput{
		{Index: 0, SlideID: "s1", DurationMs: 6000, BaseImage: "pages/p0.png",
			Elements: []Element{{ID: "shape-1", Kind: animation.KindTitle, Bounds: emuBox(96, 54, 960, 540)}},
			Revision: revisionOf("business-calm", 1, &animation.Track{
				ElementID: "shape-1", Kind: animation.KindTitle,
				Clips: []*animation.Clip{{ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 8000,
					Easing: "easeOut", Trigger: "onEnter", Continues: true}},
			})},
	}
	plan, err := c.Compile(pages)
	if err != nil {
		t.Fatal(err)
	}
	foundMissing := false
	for _, d := range plan.Warnings {
		if d.Code == DiagContinuationTargetMissing {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("最后一页 continues 溢出必须记 continuation_target_missing，got %+v", plan.Warnings)
	}
}

func TestCompileContinuationFitsNoSplit(t *testing.T) {
	c := newTestCompiler(t)
	// continues=true 但 clip 完全落在页内（无溢出）→ 不拆分，单段完整播放。
	pages := makeContinuationPages(&animation.Clip{
		ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 3000,
		Easing: "easeOut", Trigger: "onEnter", Continues: true,
	})
	plan, err := c.Compile(pages)
	if err != nil {
		t.Fatal(err)
	}
	p0 := findLayerByElementID(plan.Pages[0].Layers, "shape-1")
	if p0 == nil || len(p0.Clips) != 1 {
		t.Fatalf("页 0 应只有 1 段，got %+v", p0)
	}
	if p0.Clips[0].DurationMs != 3000 {
		t.Fatalf("页 0 段时长应为完整 3000，got %d", p0.Clips[0].DurationMs)
	}
	p1 := findLayerByElementID(plan.Pages[1].Layers, "shape-1")
	if p1 != nil {
		for _, m := range p1.Clips {
			if m.IsContinuation {
				t.Fatalf("未溢出不应产生 continuation 段")
			}
		}
	}
}
