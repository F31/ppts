package render

import (
	"sort"
	"strconv"
	"strings"
)

// focusFadeDefaultMS 与 shared/motion.ts 的 FOCUS_FADE_MS 相同。
//
// IR 里 FadeMs 为 0 时 JSON 会省略该字段，播放器把它当成 240ms，不是硬切。
// 运动窗口必须用同一个数，否则尾段会从淡入还没结束的那一帧开始冻结。
const focusFadeDefaultMS int64 = 240

// chromeMotionFrames 返回这一页要交给 Chrome 的前缀帧数。
//
// 只在「最后一笔变化之后还剩至少 1 秒」时切开：Chrome 画到最后一笔结束的那一帧，
// 余下的配音用这一帧铺满。中段的静止画面仍在这个前缀里，因为后面还有变化，
// 不能提前冻结。切不开时 motion == total，调用方整页交给 Chrome。
func chromeMotionFrames(page Page, fps int, hasPrev bool) (motion, total int, split bool) {
	if fps <= 0 {
		fps = 30
	}
	total = framesForDuration(page.DurationMs, fps)
	var end int64
	extend := func(ms int64) {
		if ms > end {
			end = ms
		}
	}
	if hasPrev {
		if tr := effectiveTransition(page.Transition); tr != nil {
			extend(tr.DurationMs)
		}
	}
	for _, layer := range page.Layers {
		for _, clip := range layer.Clips {
			extend(clip.StartMs + clip.DurationMs)
		}
	}
	for _, span := range page.Focus {
		fade := span.FadeMs
		if fade <= 0 {
			fade = focusFadeDefaultMS
		}
		extend(span.StartMs + fade)
		if span.EndMs > 0 {
			extend(span.EndMs + fade)
		}
	}
	for _, shot := range page.Shots {
		if shot.CrossFadeMs > 0 {
			extend(shot.StartMs + int64(shot.CrossFadeMs))
		}
		if n := len(shot.Camera); n >= 2 {
			extend(shot.StartMs + shot.Camera[n-1].AtMs)
		}
	}
	if end <= 0 {
		return total, total, false
	}
	motion = int(end*int64(fps)/1000) + 1
	if motion < 1 {
		motion = 1
	}
	if motion >= total {
		return total, total, false
	}
	// 不足一秒的尾段，重编码的开销大于省下的帧。
	if total-motion < fps {
		return total, total, false
	}
	return motion, total, true
}

// motionFrameList 是 --motion-frames 的参数：只列出真正切开的页。
func motionFrameList(plan *Timeline, pages []int) map[int]int {
	out := map[int]int{}
	if plan == nil {
		return out
	}
	for _, i := range pages {
		if i < 0 || i >= len(plan.Pages) {
			continue
		}
		motion, _, split := chromeMotionFrames(plan.Pages[i], plan.FPS, i > 0)
		if split {
			out[i] = motion
		}
	}
	return out
}

func formatMotionFrames(motion map[int]int) string {
	if len(motion) == 0 {
		return ""
	}
	indexes := make([]int, 0, len(motion))
	for i, n := range motion {
		if n > 0 {
			indexes = append(indexes, i)
		}
	}
	sort.Ints(indexes)
	parts := make([]string, len(indexes))
	for n, i := range indexes {
		parts[n] = strconv.Itoa(i) + ":" + strconv.Itoa(motion[i])
	}
	return strings.Join(parts, ",")
}
