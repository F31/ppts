package textnorm

import (
	"math"
)

// Legacy markup syntax inherited from the PPTS era (V3.1 §10.2):
//
//	〔读：x〕  reading marker: x is the reading text to send to TTS
//	‖        pause marker: emit a control event, remove the mark
//
// Legacy markers are opt-in via Request.LegacyMarkup; plain text input does not
// auto-interpret these characters.
const (
	readingOpen  = "〔读："
	readingClose = "〕"
	pauseMark    = "‖"
)

var readingOpenRunes = []rune(readingOpen)

// MarkupReading is one legacy reading marker 〔读：x〕 in the original text.
type MarkupReading struct {
	Outer Range // full marker span in original runes
	Inner Range // content span in original runes
	Text  string
}

// Markup is the parsed legacy marker structure of one input.
type Markup struct {
	Readings []MarkupReading
	Pauses   []int // original rune index of each ‖
	Display  string
	SrcIndex []int // display rune → original rune index
}

// ParseLegacyMarkers scans legacy 〔读：x〕 and ‖ markers.
// An unclosed 〔读：... is kept as literal text (no characters are dropped).
// Content inside a reading marker is raw until the first 〕 and is not scanned.
func ParseLegacyMarkers(text string) Markup {
	runes := []rune(text)
	var m Markup
	var disp []rune
	i := 0
	for i < len(runes) {
		if hasPrefixRunes(runes, i, readingOpenRunes) {
			end := -1
			for k := i + len(readingOpenRunes); k < len(runes); k++ {
				if runes[k] == '〕' {
					end = k
					break
				}
			}
			if end >= 0 {
				j := i + len(readingOpenRunes)
				m.Readings = append(m.Readings, MarkupReading{
					Outer: Range{i, end + 1},
					Inner: Range{j, end},
					Text:  string(runes[j:end]),
				})
				for t := j; t < end; t++ {
					disp = append(disp, runes[t])
					m.SrcIndex = append(m.SrcIndex, t)
				}
				i = end + 1
				continue
			}
		}
		if runes[i] == '‖' {
			m.Pauses = append(m.Pauses, i)
			i++
			continue
		}
		disp = append(disp, runes[i])
		m.SrcIndex = append(m.SrcIndex, i)
		i++
	}
	m.Display = string(disp)
	return m
}

func hasPrefixRunes(runes []rune, start int, prefix []rune) bool {
	if start+len(prefix) > len(runes) {
		return false
	}
	for k := 0; k < len(prefix); k++ {
		if runes[start+k] != prefix[k] {
			return false
		}
	}
	return true
}

// StripLegacyMarkers returns the display text (readings expanded, pauses removed)
// and the original rune index of each display rune. It is the subtitle/display
// side of the legacy compatibility parser.
func StripLegacyMarkers(text string) (string, []int) {
	m := ParseLegacyMarkers(text)
	return m.Display, m.SrcIndex
}

// MapSrcToDst maps an original rune position to an effective-text rune position.
// Equal-length spans map linearly; unequal-length replacement spans are estimated
// proportionally. This is an approximate compatibility projection for legacy
// display alignment (V3.1 §9.3), not a precise per-rune character mapping.
func (m SourceMap) MapSrcToDst(src int) (int, bool) {
	for _, s := range m.Spans {
		if s.Src.Start <= src && src < s.Src.End {
			if s.Src.End-s.Src.Start == s.Dst.End-s.Dst.Start {
				return s.Dst.Start + (src - s.Src.Start), true
			}
			if s.Dst.End <= s.Dst.Start {
				return s.Dst.Start, true
			}
			span := s.Src.End - s.Src.Start
			frac := float64(src-s.Src.Start) / float64(span)
			d := s.Dst.Start + int(math.Round(frac*float64(s.Dst.End-s.Dst.Start)))
			if d >= s.Dst.End {
				d = s.Dst.End - 1
			}
			return d, true
		}
	}
	return 0, false
}

// DisplayToEffective maps each display-text rune to its effective-text rune index,
// the bridge PPTS uses for subtitle/timeline char-to-token alignment. Positions
// inside unequal-length replacement spans are estimated (compatibility projection).
func DisplayToEffective(original string, res Result) []int {
	_, srcIdx := StripLegacyMarkers(original)
	out := make([]int, len(srcIdx))
	for i, s := range srcIdx {
		if d, ok := res.SourceMap.MapSrcToDst(s); ok {
			out[i] = d
		}
	}
	return out
}
