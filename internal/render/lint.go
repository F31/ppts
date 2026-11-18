package render

import (
	"fmt"
	"sort"
)

// LintOptions 是编译期动效预算。默认值刻意保守：只拦明显过密/过短的页，
// 不把正常的分段错落当成错误。
type LintOptions struct {
	MaxActiveMotions int
	MaxFocusSwitches int
	MinFocusDwellMs  int64
}

// DefaultLintOptions 返回 P0 的默认预算。后续可由租户/预设覆盖。
func DefaultLintOptions() LintOptions {
	return LintOptions{
		MaxActiveMotions: 6,
		MaxFocusSwitches: 12,
		MinFocusDwellMs:  600,
	}
}

// LintTimeline 在 IR 上做静态动效检查。它不渲染、不改计划，只返回诊断。
// 诊断会进入 Timeline.Warnings，不参与 planHash。
func LintTimeline(t *Timeline, opt LintOptions) []Diagnostic {
	if t == nil {
		return nil
	}
	if opt.MaxActiveMotions <= 0 {
		opt.MaxActiveMotions = DefaultLintOptions().MaxActiveMotions
	}
	if opt.MaxFocusSwitches <= 0 {
		opt.MaxFocusSwitches = DefaultLintOptions().MaxFocusSwitches
	}
	if opt.MinFocusDwellMs <= 0 {
		opt.MinFocusDwellMs = DefaultLintOptions().MinFocusDwellMs
	}
	var out []Diagnostic
	for i := range t.Pages {
		p := &t.Pages[i]
		if active := maxActiveMotions(p); active > opt.MaxActiveMotions {
			out = append(out, Diagnostic{
				Code: DiagMotionBudget, Page: p.Index,
				Message: fmt.Sprintf("同一时刻 %d 个元素在动，超过预算 %d", active, opt.MaxActiveMotions),
			})
		}
		if len(p.Focus) > opt.MaxFocusSwitches {
			out = append(out, Diagnostic{
				Code: DiagMotionBudget, Page: p.Index,
				Message: fmt.Sprintf("焦点切换 %d 次，超过预算 %d", len(p.Focus), opt.MaxFocusSwitches),
			})
		}
		for j := 1; j < len(p.Focus); j++ {
			gap := p.Focus[j].StartMs - p.Focus[j-1].StartMs
			if gap >= 0 && gap < opt.MinFocusDwellMs {
				out = append(out, Diagnostic{
					Code: DiagFocusDwell, Page: p.Index,
					ElementID: p.Focus[j].ElementID,
					Message:   fmt.Sprintf("焦点只停留 %dms，低于 %dms", gap, opt.MinFocusDwellMs),
				})
			}
		}
	}
	return out
}

func maxActiveMotions(p *Page) int {
	type event struct {
		at    int64
		delta int
	}
	var events []event
	for _, layer := range p.Layers {
		for _, clip := range layer.Clips {
			if clip.DurationMs <= 0 {
				continue
			}
			start := clip.StartMs
			if start < 0 {
				start = 0
			}
			end := start + clip.DurationMs
			if end > p.DurationMs {
				end = p.DurationMs
			}
			if end <= start {
				continue
			}
			events = append(events, event{at: start, delta: 1}, event{at: end, delta: -1})
		}
	}
	if len(events) == 0 {
		return 0
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].at != events[j].at {
			return events[i].at < events[j].at
		}
		return events[i].delta < events[j].delta
	})
	active, max := 0, 0
	for _, e := range events {
		active += e.delta
		if active > max {
			max = active
		}
	}
	return max
}
