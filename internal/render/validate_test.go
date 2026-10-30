package render

import (
	"errors"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func validPlan() *Timeline {
	return &Timeline{
		SchemaVersion: IRSchemaVersion,
		FPS:           30,
		Width:         1920,
		Height:        1080,
		Background:    "#FFFFFF",
		Pages: []Page{{
			Index:      0,
			SlideID:    "slide-1",
			DurationMs: 6000,
			BaseImage:  "pages/p0.png",
			Transition: Transition{Type: animation.TransitionNone},
			Layers: []Layer{{
				ElementID: "e1",
				Kind:      animation.KindTitle,
				AssetMode: AssetBitmapRegion,
				Box:       Box{X: 96, Y: 72, W: 1200, H: 200},
				Clips: []Motion{{
					ClipID: "c1", Effect: "fadeIn",
					StartMs: 0, DurationMs: 500,
					Easing: animation.EasingCurves["easeOut"],
					Props:  []animation.MotionProp{{Name: animation.PropOpacity, From: 0, To: 1}},
				}},
			}},
		}},
	}
}

func TestValidPlanPasses(t *testing.T) {
	if err := validPlan().Validate(); err != nil {
		t.Fatalf("合法计划不应报错: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Timeline)
		wantCode string
		wantPath string
	}{
		{"schema version", func(p *Timeline) { p.SchemaVersion = "renderplan/v1" }, CodeUnsupported, "schemaVersion"},
		{"fps low", func(p *Timeline) { p.FPS = 5 }, CodeInvalidPlan, "fps"},
		{"fps high", func(p *Timeline) { p.FPS = 120 }, CodeInvalidPlan, "fps"},
		{"odd width", func(p *Timeline) { p.Width = 1921 }, CodeInvalidPlan, "width"},
		{"zero height", func(p *Timeline) { p.Height = 0 }, CodeInvalidPlan, "width"},
		{"no pages", func(p *Timeline) { p.Pages = nil }, CodeInvalidPlan, "pages"},
		{"empty background", func(p *Timeline) { p.Background = " " }, CodeInvalidPlan, "background"},
		{"zero duration", func(p *Timeline) { p.Pages[0].DurationMs = 0 }, CodeInvalidPlan, "pages[0].durationMs"},
		{"empty slide id", func(p *Timeline) { p.Pages[0].SlideID = "" }, CodeInvalidPlan, "pages[0].slideId"},
		{"absolute image", func(p *Timeline) { p.Pages[0].BaseImage = "/etc/passwd" }, CodeUnsafePath, "pages[0].baseImage"},
		{"traversal", func(p *Timeline) { p.Pages[0].BaseImage = "../../secret.png" }, CodeUnsafePath, "pages[0].baseImage"},
		{"drive letter", func(p *Timeline) { p.Pages[0].BaseImage = "C:/x.png" }, CodeUnsafePath, "pages[0].baseImage"},
		{"backslash", func(p *Timeline) { p.Pages[0].BaseImage = `pages\p0.png` }, CodeUnsafePath, "pages[0].baseImage"},
		{"bad transition", func(p *Timeline) {
			p.Pages[0].Transition = Transition{Type: "wipe", DurationMs: 200}
		}, CodeInvalidPlan, "pages[0].transition"},
		{"transition too long", func(p *Timeline) {
			p.Pages[0].Transition = Transition{Type: animation.TransitionFade, DurationMs: 900}
		}, CodeInvalidPlan, "pages[0].transition"},
		{"unknown kind", func(p *Timeline) { p.Pages[0].Layers[0].Kind = "video" }, CodeInvalidPlan, "pages[0].layers[0].kind"},
		{"unknown asset mode", func(p *Timeline) {
			p.Pages[0].Layers[0].AssetMode = "vector-text"
		}, CodeUnsupported, "pages[0].layers[0].assetMode"},
		{"box outside", func(p *Timeline) { p.Pages[0].Layers[0].Box.X = 1000 }, CodeInvalidPlan, "pages[0].layers[0].box"},
		{"negative box", func(p *Timeline) { p.Pages[0].Layers[0].Box.W = 0 }, CodeInvalidPlan, "pages[0].layers[0].box"},
		{"unsafe layer image", func(p *Timeline) { p.Pages[0].Layers[0].Image = "../../etc/passwd" },
			CodeUnsafePath, "pages[0].layers[0].image"},
		{"image on page layer", func(p *Timeline) {
			p.Pages[0].Layers[0].AssetMode = AssetPageLayer
			p.Pages[0].Layers[0].Image = "layers/layer_000_00.png"
		}, CodeInvalidPlan, "pages[0].layers[0].image"},
		{"clip beyond page", func(p *Timeline) { p.Pages[0].Layers[0].Clips[0].StartMs = 5900 },
			CodeInvalidPlan, "pages[0].layers[0].clips[0]"},
		{"clip zero duration", func(p *Timeline) { p.Pages[0].Layers[0].Clips[0].DurationMs = 0 },
			CodeInvalidPlan, "pages[0].layers[0].clips[0].durationMs"},
		{"no props", func(p *Timeline) { p.Pages[0].Layers[0].Clips[0].Props = nil },
			CodeInvalidPlan, "pages[0].layers[0].clips[0].props"},
		{"unknown prop", func(p *Timeline) {
			p.Pages[0].Layers[0].Clips[0].Props[0].Name = "rotate"
		}, CodeUnsupported, "pages[0].layers[0].clips[0].props[0].name"},
		{"bad origin", func(p *Timeline) {
			p.Pages[0].Layers[0].Origin = "middle"
		}, CodeInvalidPlan, "pages[0].layers[0].origin"},
		{"bad easing", func(p *Timeline) {
			p.Pages[0].Layers[0].Clips[0].Easing = animation.CubicBezier{X1: 1.4, Y1: 0, X2: 1, Y2: 1}
		}, CodeInvalidPlan, "pages[0].layers[0].clips[0].easing"},

		// 页级焦点：三种错法各有各的后果，都必须在这里被挡住。
		{"focus unknown element", func(p *Timeline) {
			p.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "nope"}}
		}, CodeInvalidPlan, "pages[0].focus[0].elementId"},
		{"focus duplicate element", func(p *Timeline) {
			p.Pages[0].Focus = []FocusSpan{
				{StartMs: 0, ElementID: "e1"}, {StartMs: 500, ElementID: "e1"},
			}
		}, CodeInvalidPlan, "pages[0].focus[1].elementId"},
		{"focus not increasing", func(p *Timeline) {
			p.Pages[0].Focus = []FocusSpan{
				{StartMs: 500, ElementID: "e1"}, {StartMs: 500, ElementID: "e1"},
			}
		}, CodeInvalidPlan, "pages[0].focus[1]"},
		{"focus beyond page", func(p *Timeline) {
			p.Pages[0].Focus = []FocusSpan{{StartMs: 6000, ElementID: "e1"}}
		}, CodeInvalidPlan, "pages[0].focus[0].startMs"},
		{"focus fade too long", func(p *Timeline) {
			p.Pages[0].Focus = []FocusSpan{{StartMs: 0, ElementID: "e1", FadeMs: MaxFocusFadeMS + 1}}
		}, CodeInvalidPlan, "pages[0].focus[0].fadeMs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := validPlan()
			tc.mutate(p)
			err := p.Validate()
			if err == nil {
				t.Fatal("应当被拒绝")
			}
			var pe *PlanError
			if !errors.As(err, &pe) {
				t.Fatalf("错误类型 = %T, want *PlanError", err)
			}
			if pe.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%s)", pe.Code, tc.wantCode, pe.Message)
			}
			if !strings.HasPrefix(pe.Field, tc.wantPath) {
				t.Fatalf("field = %q, want prefix %q", pe.Field, tc.wantPath)
			}
		})
	}
}

// TestClipToleratesOneFrameOverrun 固定 V3 的 1 帧容差：规格允许，IR 校验也必须允许，
// 否则"合法规格被渲染器拒绝"。
func TestClipToleratesOneFrameOverrun(t *testing.T) {
	p := validPlan()
	p.Pages[0].DurationMs = 1000
	p.Pages[0].Layers[0].Clips[0] = Motion{
		ClipID: "c1", Effect: "fadeIn",
		StartMs: 900, DurationMs: 133, // 1033 > 1000，但只超过一帧（33ms）
		Easing: animation.EasingCurves["easeOut"],
		Props:  []animation.MotionProp{{Name: animation.PropOpacity, From: 0, To: 1}},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("一帧容差内应通过: %v", err)
	}
	p.Pages[0].Layers[0].Clips[0].DurationMs = 200 // 1100，超过两帧
	if err := p.Validate(); err == nil {
		t.Fatal("超过一帧容差应被拒绝")
	}
}

func TestSafeAssetPath(t *testing.T) {
	ok := []string{"p0.png", "pages/p0.png", "a/b/c.jpg"}
	for _, p := range ok {
		if err := SafeAssetPath(p); err != nil {
			t.Fatalf("%q 应合法: %v", p, err)
		}
	}
	bad := []string{"", " ", "/abs.png", "../up.png", "a/../../b.png", `a\b.png`, "C:/x.png", "c:path"}
	for _, p := range bad {
		if err := SafeAssetPath(p); err == nil {
			t.Fatalf("%q 应被拒绝", p)
		}
	}
}

// TestFocusPasses：合法的焦点序列不应报错。它不改动效，只描述「此刻讲的是谁」，
// 因此 clip 一条都不用动——这条断言同时固定了「焦点不是动效」这个设计。
func TestFocusPasses(t *testing.T) {
	p := validPlan()
	p.Pages[0].Focus = []FocusSpan{{StartMs: 1000, ElementID: "e1", FadeMs: 120}}
	if err := p.Validate(); err != nil {
		t.Fatalf("单条焦点不应报错: %v", err)
	}
}
