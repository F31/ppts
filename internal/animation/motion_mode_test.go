package animation

import "testing"

func TestInferMotionModeExplicitWins(t *testing.T) {
	rev := &Revision{
		MotionMode: MotionModeGaze,
		Tracks: []*Track{{
			ElementID: "a", Kind: KindBullet,
			Clips: []*Clip{{Type: "fadeIn", DurationMs: 400, Trigger: "onEnter"}},
		}},
	}
	if got := InferMotionMode(rev); got != MotionModeGaze {
		t.Fatalf("explicit gaze lost to inference: %q", got)
	}
}

func TestInferMotionModeEntranceWhenOnlyFadeIn(t *testing.T) {
	rev := &Revision{
		PresetID: "teaching-clear",
		Tracks: []*Track{{
			ElementID: "a", Kind: KindBullet,
			Clips: []*Clip{{Type: "fadeIn", DurationMs: 400, Trigger: "onEnter"}},
		}},
	}
	if got := InferMotionMode(rev); got != MotionModeEntrance {
		t.Fatalf("old fadeIn spec = %q, want entrance", got)
	}
}

func TestInferMotionModeGazeWhenOnlySpotlight(t *testing.T) {
	rev := &Revision{
		Tracks: []*Track{{
			ElementID: "a", Kind: KindBullet,
			Clips: []*Clip{{Type: "spotlight", DurationMs: 240, Trigger: "withSegment:s"}},
		}},
	}
	if got := InferMotionMode(rev); got != MotionModeGaze {
		t.Fatalf("spotlight-only = %q, want gaze", got)
	}
}

func TestInferMotionModeGazeEntranceWhenBoth(t *testing.T) {
	rev := &Revision{
		Tracks: []*Track{{
			ElementID: "a", Kind: KindBullet,
			Clips: []*Clip{
				{Type: "spotlight", DurationMs: 240, Trigger: "withSegment:s"},
				{Type: "fadeIn", DurationMs: 400, Trigger: "withSegment:s"},
			},
		}},
	}
	if got := InferMotionMode(rev); got != MotionModeGazeEntrance {
		t.Fatalf("both = %q, want gaze+entrance", got)
	}
}

func TestInferMotionModeEmptyIsGaze(t *testing.T) {
	if got := InferMotionMode(&Revision{}); got != MotionModeGaze {
		t.Fatalf("empty spec = %q, want gaze", got)
	}
	if got := InferMotionMode(nil); got != MotionModeGaze {
		t.Fatalf("nil = %q, want gaze", got)
	}
}
