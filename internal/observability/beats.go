package observability

import (
	"strconv"

	"expvar"
)

// 节拍表观测。键格式与预填相同（k=v），不上 Prometheus histogram。
var (
	beatsSourceTotal = expvar.NewMap("ppts_animation_beats_total")
	beatsLooksTotal  = expvar.NewMap("ppts_animation_beats_looks_total")
	beatsDeniedTotal = expvar.NewMap("ppts_animation_beats_denied_total")
)

// BeatsSource 记录一页拍表切拍来源：vad | punct | fallback。
func BeatsSource(source string) {
	if source == "" {
		return
	}
	beatsSourceTotal.Add("source="+clean(source), 1)
}

// BeatsLooks 记录一页最终 look 数桶：0–5 或 6plus。
func BeatsLooks(n int) {
	key := "looks=" + looksBucket(n)
	beatsLooksTotal.Add(key, 1)
}

// BeatsDenied 记录被命名规则丢掉的候选（page_number | aux | area）。
func BeatsDenied(reason string, n int) {
	if reason == "" || n <= 0 {
		return
	}
	beatsDeniedTotal.Add("reason="+clean(reason), int64(n))
}

func looksBucket(n int) string {
	if n < 0 {
		n = 0
	}
	if n >= 6 {
		return "6plus"
	}
	return strconv.Itoa(n)
}
