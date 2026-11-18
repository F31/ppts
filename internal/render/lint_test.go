package render

import "testing"

func TestLintMotionBudget(t *testing.T) {
	plan := &Timeline{Pages: []Page{{
		Index: 0, DurationMs: 4000,
		Layers: []Layer{
			{ElementID: "a", Clips: []Motion{{StartMs: 0, DurationMs: 400}}},
			{ElementID: "b", Clips: []Motion{{StartMs: 140, DurationMs: 400}}},
			{ElementID: "c", Clips: []Motion{{StartMs: 280, DurationMs: 400}}},
		},
	}}}
	diags := LintTimeline(plan, LintOptions{MaxActiveMotions: 1, MaxFocusSwitches: 12, MinFocusDwellMs: 600})
	if !hasDiag(diags, DiagMotionBudget) {
		t.Fatalf("density violation not reported: %+v", diags)
	}
}

func TestLintFocusDwell(t *testing.T) {
	plan := &Timeline{Pages: []Page{{
		Index: 0, DurationMs: 4000,
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a"},
			{StartMs: 300, ElementID: "b"},
		},
	}}}
	diags := LintTimeline(plan, LintOptions{MaxActiveMotions: 6, MaxFocusSwitches: 12, MinFocusDwellMs: 600})
	if !hasDiag(diags, DiagFocusDwell) {
		t.Fatalf("focus dwell violation not reported: %+v", diags)
	}
}
