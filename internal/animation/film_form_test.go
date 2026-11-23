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
