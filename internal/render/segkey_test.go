package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

// 本文件的判据都是「键变不变」，不是「键等不等于某个字面量」：
// 后者一改结构就假红，而且看不出意图。每条用例对应一句「什么变了像素就变」。

func multiPagePlan(n int) *Timeline {
	pages := make([]Page, n)
	for i := range pages {
		pages[i] = Page{
			Index:      i,
			SlideID:    slideID(i),
			DurationMs: 3000,
			BaseImage:  "pages/p" + string(rune('0'+i)) + ".png",
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
		}
	}
	return &Timeline{
		SchemaVersion: IRSchemaVersion,
		FPS:           30,
		Width:         1920,
		Height:        1080,
		Background:    "#FFFFFF",
		Pages:         pages,
	}
}

func slideID(i int) string { return "slide-" + string(rune('a'+i)) }

func mustKeys(t *testing.T, plan *Timeline, env SegmentEnv) []string {
	t.Helper()
	keys, err := SegmentKeys(plan, env)
	if err != nil {
		t.Fatalf("SegmentKeys: %v", err)
	}
	return keys
}

func testEnv() SegmentEnv {
	return SegmentEnv{RendererVersion: "1.0.0", Encoding: "crf=18;gop=30", ChromeVersion: "Chrome/156"}
}

func TestSegmentKeysAreDeterministicHex(t *testing.T) {
	plan := multiPagePlan(3)
	a := mustKeys(t, plan, testEnv())
	b := mustKeys(t, plan, testEnv())
	if len(a) != 3 {
		t.Fatalf("keys = %d, want 3", len(a))
	}
	for i := range a {
		if len(a[i]) != 64 || !validSegmentKey(a[i]) {
			t.Fatalf("key %d is not 64 lowercase hex: %q", i, a[i])
		}
		if a[i] != b[i] {
			t.Fatalf("key %d not deterministic", i)
		}
	}
	if a[0] == a[1] {
		t.Fatal("different pages must not share a key")
	}
}

// 页序号与 slideId 不参与键：同一页换个位置、换个 id，像素没变，就该复用。
func TestSegmentKeyIgnoresPositionAndSlideID(t *testing.T) {
	plan := multiPagePlan(3)
	base := mustKeys(t, plan, testEnv())

	moved := multiPagePlan(3)
	// 把第 0 页挪到第 1 位，镜像改 Index 与 slideId（页面内容一字不动）。
	moved.Pages[0], moved.Pages[1] = moved.Pages[1], moved.Pages[0]
	for i := range moved.Pages {
		moved.Pages[i].Index = i
		moved.Pages[i].SlideID = slideID(i)
	}
	keys := mustKeys(t, moved, testEnv())
	// 原第 0 页现在在位置 1；两者内容相同、都没有转场依赖，键必须相同。
	if keys[1] != base[0] {
		t.Fatalf("moving a page changed its key:\n base[0]=%s\n new[1] =%s", base[0], keys[1])
	}
}

func TestSegmentKeyInvalidatesChangedPageAndTransitiveSuccessor(t *testing.T) {
	plan := multiPagePlan(4)
	plan.Pages[2].Transition = Transition{Type: animation.TransitionFade, DurationMs: 300}
	plan.Pages[3].Transition = Transition{Type: animation.TransitionFade, DurationMs: 300}
	before := mustKeys(t, plan, testEnv())

	// 只改第 1 页的动画时长。
	plan.Pages[1].Layers[0].Clips[0].DurationMs = 900
	after := mustKeys(t, plan, testEnv())

	if after[1] == before[1] {
		t.Fatal("changed page must miss")
	}
	if after[2] == before[2] {
		t.Fatal("successor with an active transition must miss (it draws the previous page's last frame)")
	}
	if after[0] != before[0] {
		t.Fatal("unrelated page must still hit")
	}
	if after[3] != before[3] {
		t.Fatal("page beyond the successor must still hit")
	}
}

func TestSegmentKeyWithoutTransitionIgnoresPrev(t *testing.T) {
	plan := multiPagePlan(2)
	before := mustKeys(t, plan, testEnv())
	plan.Pages[0].Layers[0].Clips[0].DurationMs = 900
	after := mustKeys(t, plan, testEnv())
	if after[1] != before[1] {
		t.Fatal("page without a transition must not depend on the previous page")
	}
}

func TestSegmentKeyOfFirstPageHasNoPrev(t *testing.T) {
	plan := multiPagePlan(2)
	// 两页内容与转场完全一致：唯一的差别是「第 0 页没有前一页」。
	plan.Pages[1] = plan.Pages[0]
	plan.Pages[1].Index = 1
	plan.Pages[1].SlideID = "slide-x"
	plan.Pages[0].Transition = Transition{Type: animation.TransitionFade, DurationMs: 300}
	plan.Pages[1].Transition = Transition{Type: animation.TransitionFade, DurationMs: 300}
	keys := mustKeys(t, plan, testEnv())
	if keys[0] == keys[1] {
		t.Fatal("page 0 renders without a transition; it must not share a key with a visually different successor")
	}
}

func TestSegmentKeyEnvInvalidatesEverything(t *testing.T) {
	plan := multiPagePlan(3)
	base := mustKeys(t, plan, testEnv())
	cases := map[string]SegmentEnv{
		"renderer version": {RendererVersion: "1.0.1", Encoding: "crf=18;gop=30", ChromeVersion: "Chrome/156"},
		"crf":              {RendererVersion: "1.0.0", Encoding: "crf=20;gop=30", ChromeVersion: "Chrome/156"},
		"chrome":           {RendererVersion: "1.0.0", Encoding: "crf=18;gop=30", ChromeVersion: "Chrome/157"},
	}
	for name, env := range cases {
		keys := mustKeys(t, plan, env)
		for i := range keys {
			if keys[i] == base[i] {
				t.Fatalf("%s change must invalidate page %d", name, i)
			}
		}
	}
}

func TestSegmentKeyIgnoresDerivedAndAuditFields(t *testing.T) {
	plan := multiPagePlan(2)
	before := mustKeys(t, plan, testEnv())

	// 派生物：底板与层图是 baseImage + boxes 的函数，路径随工作目录变化。
	plan.Pages[0].PlateImage = "plates/plate_000.png"
	plan.Pages[0].Layers[0].Image = "layers/layer_000_00.png"
	// 审计字段：IR 规范明写后端 MUST NOT 读 effect 决定行为。
	plan.Pages[0].Layers[0].Clips[0].ClipID = "c9"
	plan.Pages[0].Layers[0].Clips[0].Effect = "slideUp"

	after := mustKeys(t, plan, testEnv())
	if after[0] != before[0] {
		t.Fatal("derived paths and audit-only fields must not participate in the key")
	}
}

func TestSegmentKeyNormalizesNilAndEmptyLayers(t *testing.T) {
	plan := multiPagePlan(2)
	base := mustKeys(t, plan, testEnv())

	plan.Pages[0].Layers = nil
	nilKeys := mustKeys(t, plan, testEnv())
	if nilKeys[0] == base[0] {
		t.Fatal("a page with layers and one without must not share a key")
	}

	empty := multiPagePlan(1)
	empty.Pages[0].Layers = []Layer{}
	emptyKeys := mustKeys(t, empty, testEnv())
	if emptyKeys[0] != nilKeys[0] {
		t.Fatal("nil and [] layers serialize differently but render identically; keys must match")
	}
}

// 0ms 的转场与 none 像素相同（合成器要求 durationMs > 0）。
func TestSegmentKeyNormalizesZeroDurationTransition(t *testing.T) {
	none := multiPagePlan(2)
	zero := multiPagePlan(2)
	zero.Pages[1].Transition = Transition{Type: animation.TransitionFade, DurationMs: 0}
	if mustKeys(t, none, testEnv())[1] != mustKeys(t, zero, testEnv())[1] {
		t.Fatal("a 0ms transition renders nothing; it must not produce a different key")
	}
}

func TestSegmentEnvStringIsUnambiguous(t *testing.T) {
	a := SegmentEnv{RendererVersion: "v;encoding=x", Encoding: "y"}
	b := SegmentEnv{RendererVersion: "v", Encoding: "x;encoding=y"}
	if a.String() == b.String() {
		t.Fatalf("环境串必须带字段前缀，否则拼接歧义会撞键: %q", a.String())
	}
}
