package render

import (
	"strings"
	"unicode"

	"github.com/F31/ppts/internal/animation"
)

const (
	pointerHPx      = 6
	pointerGapPx    = 4
	pointerMinWPx   = 24
	lookPhraseGapMS = 800
	minPhraseHoldMS = 1600
	maxPhraseLooks  = 2
	// denseGazeLooks 六宫格这类点名页：互异形状已经够密，再为词级指针加仍帧
	// 会把 2G 渲染器打到 OOM。稀疏页（<6 个互异 id）仍允许 hold 加拍。
	denseGazeLooks = 6
)

// pointerRect 在文本盒底边画一条短下划线。spoken 是当前拍旁白；能对上形状原文时
// 只覆盖那一截，否则整行。页图保持 1:1，这条线就是词级指针。
func pointerRect(box Box, shapeText, spoken string) *Box {
	if box.W < pointerMinWPx || box.H < pointerHPx+pointerGapPx {
		return nil
	}
	from, to := 0.0, 1.0
	if a, b, ok := runeSpan(shapeText, spoken); ok {
		n := float64(len([]rune(compactRunes(shapeText))))
		if n > 0 && b > a {
			from, to = float64(a)/n, float64(b)/n
		}
	}
	if to-from < 0.12 {
		mid := (from + to) / 2
		from = mid - 0.08
		if from < 0 {
			from = 0
		}
		to = from + 0.16
		if to > 1 {
			to = 1
			from = 0.84
		}
	}
	x := box.X + int(float64(box.W)*from)
	w := int(float64(box.W) * (to - from))
	if w < pointerMinWPx {
		w = pointerMinWPx
		if x+w > box.X+box.W {
			x = box.X + box.W - w
			if x < box.X {
				x = box.X
				w = box.W
			}
		}
	}
	y := box.Y + box.H - pointerGapPx - pointerHPx
	if y < box.Y {
		y = box.Y
	}
	return &Box{X: x, Y: y, W: w, H: pointerHPx}
}

func pointerAllowed(kind string) bool {
	switch kind {
	case animation.KindTable, animation.KindRow, animation.KindChart, animation.KindBar:
		return false
	default:
		return true
	}
}

func compactRunes(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func runeSpan(shape, spoken string) (int, int, bool) {
	sr := []rune(compactRunes(shape))
	sp := []rune(compactRunes(spoken))
	if len(sr) == 0 || len(sp) == 0 {
		return 0, 0, false
	}
	// 旁白长于形状原文时（整句对标题）回退整行，下标必须落在形状里。
	if len(sp) > len(sr) {
		return 0, 0, false
	}
	n := len(sp)
	for i := 0; i+n <= len(sr); i++ {
		match := true
		for j := 0; j < n; j++ {
			if sr[i+j] != sp[j] {
				match = false
				break
			}
		}
		if match {
			return i, i + n, true
		}
	}
	return 0, 0, false
}

// pointersFromFocus 给每条注视加词级下划线。盒子用元素原文框，不用扩到卡片底。
func pointersFromFocus(spans []FocusSpan, elements []Element, sheet *animation.BeatSheet, scale float64, canvasW, canvasH int) []Annotation {
	if len(spans) == 0 || canvasW <= 0 || canvasH <= 0 {
		return nil
	}
	byID := make(map[string]Element, len(elements))
	for _, el := range elements {
		byID[el.ID] = el
	}
	spokenAt := map[int64]string{}
	if sheet != nil {
		for _, b := range sheet.Beats {
			if strings.TrimSpace(b.Text) != "" {
				spokenAt[b.StartMs] = b.Text
			}
		}
	}
	out := make([]Annotation, 0, len(spans))
	for i, s := range spans {
		if s.Rect == nil || s.Rect.W <= 0 || s.Rect.H <= 0 {
			continue
		}
		el, ok := byID[s.ElementID]
		if ok && !pointerAllowed(el.Kind) {
			continue
		}
		box := *s.Rect
		shapeText := ""
		if ok {
			if raw, _, rawOK, _ := boxOf(el.Bounds, scale, canvasW, canvasH); rawOK && raw.W >= pointerMinWPx {
				box = raw
			}
			shapeText = el.Text
		}
		spoken := spokenAt[s.StartMs]
		rect := pointerRect(box, shapeText, spoken)
		if rect == nil {
			continue
		}
		end := s.EndMs
		if end == 0 && i+1 < len(spans) && spans[i+1].StartMs > s.StartMs {
			end = spans[i+1].StartMs
		}
		out = append(out, Annotation{
			Kind:      AnnotationPointer,
			StartMs:   s.StartMs,
			EndMs:     end,
			FadeMs:    s.FadeMs,
			ElementID: s.ElementID,
			Rect:      rect,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
