package render

import (
	"expvar"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation"
)

var (
	animationRenderDurationMS = expvar.NewMap("ppts_animation_render_duration_ms_total")
	animationRenderPagesTotal = expvar.NewMap("ppts_animation_render_pages_total")
	animationGazePagesTotal   = expvar.NewInt("ppts_animation_gaze_pages_total")
	animationEntranceSkipped  = expvar.NewInt("ppts_animation_entrance_skipped_total")
	animationDegradeTotal     = expvar.NewMap("ppts_animation_degrade_total")
	animationChromeShareAlarm = expvar.NewInt("ppts_animation_chrome_share_alarm_total")
)

func metricKey(s string) string {
	s = strings.ReplaceAll(s, ",", "_")
	return strings.ReplaceAll(s, "=", "_")
}

// RecordPathStats 记录一次渲染桶的页数与耗时（path=gaze_looks|chrome|still）。
func RecordPathStats(path string, pages int, d time.Duration) {
	if pages <= 0 {
		return
	}
	key := "path=" + metricKey(path)
	animationRenderPagesTotal.Add(key, int64(pages))
	if d > 0 {
		animationRenderDurationMS.Add(key, d.Milliseconds())
	}
	if path == RenderPathGazeLooks {
		animationGazePagesTotal.Add(int64(pages))
	}
}

// RecordGazePages 记录编译器分出的注视页数。
func RecordGazePages(n int) {
	if n <= 0 {
		return
	}
	animationGazePagesTotal.Add(int64(n))
}

// RecordEntranceSkipped 记录一次 boxesReliable 失败而拒绝挖洞。
func RecordEntranceSkipped() { animationEntranceSkipped.Add(1) }

// RecordDegrade 记录一次动画通道回退静图。
func RecordDegrade(stage string) {
	if stage == "" {
		stage = "still"
	}
	animationDegradeTotal.Add("stage="+metricKey(stage), 1)
}

// RecordChromeShareAlarm 记录一次「教学导出里 chrome 占比 > 20%」。
func RecordChromeShareAlarm() { animationChromeShareAlarm.Add(1) }

const (
	// RenderPathGazeLooks 是教学注视页：每 look 一帧 + ffmpeg hold。
	RenderPathGazeLooks = "gaze_looks"
	RenderPathChrome    = "chrome"
	RenderPathStill     = "still"

	// ChromeShareAlarmRatio 是教学导出里 chrome 占 gaze+chrome 的告警线。
	// 持续超过它说明 pageNeedsCompositor 把注视页漏进了 Chrome。
	ChromeShareAlarmRatio = 0.20
)

// SplitRenderPages 导出三桶分类，供观测与门禁夹具使用。
func SplitRenderPages(plan *Timeline, pages []int) (chrome, gaze, still []int) {
	return splitRenderPages(plan, pages)
}

// ChromeShare 是 chrome / (chrome+gaze)。没有注视页时为 0（入场片整页 chrome 不是这条告警）。
func ChromeShare(chrome, gaze int) float64 {
	total := chrome + gaze
	if total == 0 {
		return 0
	}
	return float64(chrome) / float64(total)
}

// ChromeShareAlarm 报告教学向导出的 chrome 占比是否越线。
func ChromeShareAlarm(chrome, gaze int) bool {
	return gaze > 0 && ChromeShare(chrome, gaze) > ChromeShareAlarmRatio
}

// DescribePageMotion 给出一页的 motion_mode / looks / plate，对齐设计日志字段。
func DescribePageMotion(p Page) (mode string, looks int, plate string) {
	plate = "skipped"
	if HasEntranceElementLayer(p.Layers) {
		plate = "built"
	}
	looks = len(lookWindows(p))
	switch {
	case isGazeLookPage(p) && HasEntranceElementLayer(p.Layers):
		return animation.MotionModeGazeEntrance, looks, plate
	case isGazeLookPage(p):
		return animation.MotionModeGaze, looks, plate
	case HasEntranceElementLayer(p.Layers):
		return animation.MotionModeEntrance, looks, plate
	default:
		return "still", looks, plate
	}
}
