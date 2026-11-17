package render

import (
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func TestPointerRectSubstring(t *testing.T) {
	box := Box{X: 100, Y: 200, W: 600, H: 40}
	got := pointerRect(box, "智慧城市智能办公智能制造", "智能办公")
	if got == nil {
		t.Fatal("pointerRect returned nil")
	}
	// 12 字，命中第 5–8：「智能办公」→ 1/3..2/3
	if got.X < 290 || got.X > 310 {
		t.Fatalf("x = %d, want ~300", got.X)
	}
	if got.W < 190 || got.W > 210 {
		t.Fatalf("w = %d, want ~200", got.W)
	}
	if got.Y != 200+40-pointerGapPx-pointerHPx {
		t.Fatalf("y = %d, want bottom bar", got.Y)
	}
	if got.H != pointerHPx {
		t.Fatalf("h = %d, want %d", got.H, pointerHPx)
	}
}

func TestPointerRectSpokenLongerFallsBackToLine(t *testing.T) {
	box := Box{X: 0, Y: 0, W: 400, H: 48}
	got := pointerRect(box, "智慧城市", "首先看智慧城市的建设成果")
	if got == nil {
		t.Fatal("nil")
	}
	if got.X != 0 || got.W != 400 {
		t.Fatalf("full line, got x=%d w=%d", got.X, got.W)
	}
}

func TestPointerRectTinyBoxSkipped(t *testing.T) {
	if pointerRect(Box{X: 0, Y: 0, W: 10, H: 8}, "字", "字") != nil {
		t.Fatal("tiny box must be skipped")
	}
}

func TestRuneSpanMiss(t *testing.T) {
	if _, _, ok := runeSpan("智慧城市", "智能办公"); ok {
		t.Fatal("unrelated phrase must miss")
	}
}

func TestPointersFromFocusUsesElementBox(t *testing.T) {
	c := newTestCompiler(t)
	elBox := emuBox(100, 200, 400, 80)
	card := Box{X: 80, Y: 180, W: 520, H: 280}
	spans := []FocusSpan{{
		StartMs: 0, EndMs: 4000, ElementID: "title", Rect: &card, FadeMs: 240,
	}}
	els := []Element{{
		ID: "title", Kind: animation.KindTitle, Bounds: elBox, Text: "智慧城市智能办公",
	}}
	sheet := &animation.BeatSheet{Beats: []animation.Beat{
		{StartMs: 0, EndMs: 4000, ShapeID: "title", Text: "智慧城市"},
	}}
	got := pointersFromFocus(spans, els, sheet, c.scale, 1920, 1080)
	if len(got) != 1 {
		t.Fatalf("pointers = %d, want 1: %+v", len(got), got)
	}
	if got[0].Kind != AnnotationPointer {
		t.Fatalf("kind = %q", got[0].Kind)
	}
	if got[0].Rect == nil {
		t.Fatal("missing rect")
	}
	// 元素盒 400px 宽，不是扩后的 520 卡片。
	if got[0].Rect.W > 400 {
		t.Fatalf("pointer used expanded card, w=%d", got[0].Rect.W)
	}
	if got[0].Rect.X < 100 || got[0].Rect.X > 140 {
		t.Fatalf("pointer x=%d, want near element left 100", got[0].Rect.X)
	}
}
