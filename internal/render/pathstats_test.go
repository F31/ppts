package render

import (
	"expvar"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
)

func metricMap(t *testing.T, name string) map[string]float64 {
	t.Helper()
	v := expvar.Get(name)
	if v == nil {
		t.Fatalf("%s 没有注册", name)
	}
	out := map[string]float64{}
	m, ok := v.(*expvar.Map)
	if !ok {
		t.Fatalf("%s 不是 Map", name)
	}
	m.Do(func(kv expvar.KeyValue) {
		if n, ok := kv.Value.(*expvar.Int); ok {
			out[kv.Key] = float64(n.Value())
		}
	})
	return out
}

func metricInt(t *testing.T, name string) int64 {
	t.Helper()
	v := expvar.Get(name)
	if v == nil {
		t.Fatalf("%s 没有注册", name)
	}
	return v.(*expvar.Int).Value()
}

func TestRecordPathStatsLabels(t *testing.T) {
	gazeBefore := metricInt(t, "ppts_animation_gaze_pages_total")
	pagesBefore := metricMap(t, "ppts_animation_render_pages_total")["path=gaze_looks"]
	msBefore := metricMap(t, "ppts_animation_render_duration_ms_total")["path=gaze_looks"]

	RecordPathStats(RenderPathGazeLooks, 3, 1500*time.Millisecond)
	RecordPathStats(RenderPathChrome, 1, 800*time.Millisecond)
	RecordPathStats(RenderPathStill, 0, time.Second)

	if got := metricInt(t, "ppts_animation_gaze_pages_total") - gazeBefore; got != 3 {
		t.Fatalf("gaze pages Δ = %d, want 3", got)
	}
	if got := metricMap(t, "ppts_animation_render_pages_total")["path=gaze_looks"] - pagesBefore; got != 3 {
		t.Fatalf("pages path=gaze_looks Δ = %v, want 3", got)
	}
	if got := metricMap(t, "ppts_animation_render_duration_ms_total")["path=gaze_looks"] - msBefore; got < 1500 {
		t.Fatalf("duration path=gaze_looks Δ = %v, want >= 1500", got)
	}
}

func TestRecordDegradeAndEntranceSkipped(t *testing.T) {
	skipBefore := metricInt(t, "ppts_animation_entrance_skipped_total")
	alarmBefore := metricInt(t, "ppts_animation_chrome_share_alarm_total")
	stillBefore := metricMap(t, "ppts_animation_degrade_total")["stage=still"]

	RecordEntranceSkipped()
	RecordChromeShareAlarm()
	RecordDegrade("still")
	RecordDegrade("")

	if got := metricInt(t, "ppts_animation_entrance_skipped_total") - skipBefore; got != 1 {
		t.Fatalf("entrance skipped Δ = %d, want 1", got)
	}
	if got := metricInt(t, "ppts_animation_chrome_share_alarm_total") - alarmBefore; got != 1 {
		t.Fatalf("chrome share alarm Δ = %d, want 1", got)
	}
	if got := metricMap(t, "ppts_animation_degrade_total")["stage=still"] - stillBefore; got != 2 {
		t.Fatalf("degrade still Δ = %v, want 2", got)
	}
}

func TestChromeShareAlarmOnlyWhenGazeLeaks(t *testing.T) {
	if ChromeShareAlarm(10, 0) {
		t.Fatal("纯入场（无注视页）不得因 chrome=100% 告警")
	}
	if ChromeShareAlarm(1, 10) {
		t.Fatal("1 chrome / 11 = 9% 不应告警")
	}
	if !ChromeShareAlarm(3, 10) {
		t.Fatal("3 chrome / 13 ≈ 23% 应告警")
	}
	if ChromeShare(3, 10) <= ChromeShareAlarmRatio {
		t.Fatalf("share = %v, want > %v", ChromeShare(3, 10), ChromeShareAlarmRatio)
	}
}

func TestDescribePageMotionGazeSkipsPlate(t *testing.T) {
	page := Page{
		DurationMs: 80000,
		Focus: []FocusSpan{
			{StartMs: 0, ElementID: "a", Rect: &Box{X: 8, Y: 8, W: 80, H: 40}},
			{StartMs: 40000, ElementID: "b", Rect: &Box{X: 8, Y: 80, W: 80, H: 40}, FadeMs: 240},
		},
		Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
	}
	mode, looks, plate := DescribePageMotion(page)
	if mode != animation.MotionModeGaze {
		t.Fatalf("mode = %q, want gaze", mode)
	}
	if looks != 2 {
		t.Fatalf("looks = %d, want 2 (two focus, no arrive split)", looks)
	}
	if plate != "skipped" {
		t.Fatalf("plate = %q, want skipped", plate)
	}
}

func TestDescribePageMotionGazeEntranceBuildsPlate(t *testing.T) {
	page := Page{
		DurationMs: 8000,
		Focus: []FocusSpan{{
			StartMs: 0, ElementID: "a", Rect: &Box{X: 8, Y: 8, W: 80, H: 40},
		}},
		Layers: []Layer{{
			ElementID: "a",
			Clips:     []Motion{{Effect: "fadeIn", DurationMs: 400}},
		}},
		Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
	}
	mode, looks, plate := DescribePageMotion(page)
	if mode != animation.MotionModeGazeEntrance {
		t.Fatalf("mode = %q, want gaze+entrance", mode)
	}
	if looks != 1 {
		t.Fatalf("looks = %d, want 1 (entrance skips arrive)", looks)
	}
	if plate != "built" {
		t.Fatalf("plate = %q, want built", plate)
	}
	if !isGazeLookPage(page) {
		t.Fatal("gaze+entrance with Focus.rect stays on the look chain")
	}
}

func TestSplitRenderPagesGazeNotChrome(t *testing.T) {
	plan := &Timeline{Pages: []Page{{
		DurationMs: 80000,
		Focus:      []FocusSpan{{StartMs: 0, ElementID: "a", Rect: &Box{X: 1, Y: 1, W: 10, H: 10}}},
		Transition: Transition{Type: animation.TransitionFade, DurationMs: 280},
	}}}
	chrome, gaze, still := SplitRenderPages(plan, []int{0})
	if len(gaze) != 1 || len(chrome) != 0 || len(still) != 0 {
		t.Fatalf("buckets chrome=%v gaze=%v still=%v, want gaze only", chrome, gaze, still)
	}
}
