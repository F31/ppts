package animation

import "testing"

func TestResolveFilmForm(t *testing.T) {
	form, want := ResolveFilmForm(FilmFormReveal, false)
	if form != FilmFormReveal || !want {
		t.Fatalf("reveal = %s %v", form, want)
	}
	form, want = ResolveFilmForm(FilmFormGaze, true)
	if form != FilmFormGaze || want {
		t.Fatalf("explicit gaze wins over withEntrance: %s %v", form, want)
	}
	form, want = ResolveFilmForm("", true)
	if form != FilmFormReveal || !want {
		t.Fatalf("legacy withEntrance = %s %v", form, want)
	}
	form, want = ResolveFilmForm("", false)
	if form != FilmFormGaze || want {
		t.Fatalf("default = %s %v", form, want)
	}
	if MotionModeForFilmForm(FilmFormReveal) != MotionModeGazeEntrance {
		t.Fatal("reveal maps to gaze+entrance")
	}
	if FilmFormForMotionMode(MotionModeGazeEntrance) != FilmFormReveal {
		t.Fatal("gaze+entrance is reveal")
	}
	if FilmFormForMotionMode(MotionModeEntrance) != FilmFormReveal {
		t.Fatal("entrance-only is reveal")
	}
	if !ValidFilmForm(FilmFormGaze) || ValidFilmForm("punch") {
		t.Fatal("valid forms")
	}
}

func TestReconcileFormSourceMarksPageOnChange(t *testing.T) {
	prev := &Revision{MotionMode: MotionModeGaze, FormSource: FormSourceBatch}
	next := &Revision{MotionMode: MotionModeGazeEntrance}
	ReconcileFormSource(prev, next)
	if next.FormSource != FormSourcePage {
		t.Fatalf("form change = %q, want page", next.FormSource)
	}
}

func TestReconcileFormSourceKeepsBatchWhenUnchanged(t *testing.T) {
	prev := &Revision{MotionMode: MotionModeGaze, FormSource: FormSourceBatch}
	next := &Revision{MotionMode: MotionModeGaze}
	ReconcileFormSource(prev, next)
	if next.FormSource != FormSourceBatch {
		t.Fatalf("same form = %q, want batch", next.FormSource)
	}
}

func TestReconcileFormSourceFirstRevealIsPage(t *testing.T) {
	next := &Revision{MotionMode: MotionModeGazeEntrance}
	ReconcileFormSource(nil, next)
	if next.FormSource != FormSourcePage {
		t.Fatalf("first reveal = %q, want page", next.FormSource)
	}
	gaze := &Revision{MotionMode: MotionModeGaze}
	ReconcileFormSource(nil, gaze)
	if gaze.FormSource != "" {
		t.Fatalf("first gaze = %q, want empty", gaze.FormSource)
	}
}

func TestHasPageFilmForm(t *testing.T) {
	if HasPageFilmForm(nil) || HasPageFilmForm(&Revision{FormSource: FormSourceBatch}) {
		t.Fatal("batch/nil must not skip apply-all")
	}
	if !HasPageFilmForm(&Revision{FormSource: FormSourcePage}) {
		t.Fatal("page override must skip apply-all")
	}
}
