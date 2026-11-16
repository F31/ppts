package render

import "testing"

func TestChromeMotionFramesHoldsTheTailAfterEntrance(t *testing.T) {
	page := Page{
		DurationMs: 20_000,
		Layers: []Layer{{
			Clips: []Motion{{StartMs: 0, DurationMs: 400}},
		}},
	}
	motion, total, split := chromeMotionFrames(page, 30, false)
	if total != 600 {
		t.Fatalf("total frames = %d, want 600", total)
	}
	// 400ms 落在第 12 帧（0 起算），Chrome 必须画到这一帧，共 13 帧。
	if motion != 13 || !split {
		t.Fatalf("motion=%d split=%v, want 13 and a hold", motion, split)
	}
}

func TestChromeMotionFramesKeepsLateMotionInsideChrome(t *testing.T) {
	page := Page{
		DurationMs: 20_000,
		Focus: []FocusSpan{{
			StartMs: 19_000,
		}},
	}
	motion, total, split := chromeMotionFrames(page, 30, false)
	// 焦点从 19s 起淡入 240ms，尾段不足 1 秒，整页留在 Chrome。
	if split || motion != total {
		t.Fatalf("late focus must stay on chrome: motion=%d total=%d split=%v", motion, total, split)
	}
}

func TestChromeMotionFramesIncludesOpeningTransition(t *testing.T) {
	page := Page{
		DurationMs: 10_000,
		Transition: Transition{Type: "slide", DurationMs: 280},
		Layers: []Layer{{
			Clips: []Motion{{StartMs: 0, DurationMs: 200}},
		}},
	}
	motion, _, split := chromeMotionFrames(page, 30, true)
	// 转场 280ms 比 200ms 的入场更晚结束。
	if !split || motion != int(280*30/1000)+1 {
		t.Fatalf("motion=%d split=%v, want transition end", motion, split)
	}
}

func TestFormatMotionFramesSortsAndSkipsEmpty(t *testing.T) {
	if got := formatMotionFrames(nil); got != "" {
		t.Fatalf("empty map = %q", got)
	}
	got := formatMotionFrames(map[int]int{2: 40, 0: 13, 5: 0})
	if got != "0:13,2:40" {
		t.Fatalf("got %q", got)
	}
}

func TestChromeMotionFramesIgnoresTransitionWithoutPrev(t *testing.T) {
	page := Page{
		DurationMs: 10_000,
		Transition: Transition{Type: "slide", DurationMs: 280},
		Layers: []Layer{{
			Clips: []Motion{{StartMs: 0, DurationMs: 200}},
		}},
	}
	motion, _, split := chromeMotionFrames(page, 30, false)
	if !split || motion != int(200*30/1000)+1 {
		t.Fatalf("first page must not count its entrance transition: motion=%d split=%v", motion, split)
	}
}
