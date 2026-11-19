package beats

import (
	"regexp"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

type piece struct {
	Text      string
	StartMs   int64
	EndMs     int64
	SegmentID string
	// Section / SectionID 是页面结构锚点：Section 是页内第几个编号分区，
	// SectionID 是这一区的落点形状。没有编号页时为 0 / 空。
	Section   int
	SectionID string
	// NoMerge 是显式结构边界切出的片段（如「第二部分」「第三部分」）。
	// 短拍不能跨过它合并，否则两个点名句会重新粘成一片。
	NoMerge bool
}

var ordinalClauseRe = regexp.MustCompile(`第[一二两三四五六七八九十\p{Nd}]{1,3}\s*(?:部分|步|条|点|项|章|节|块|类)`)

func holdMsOf(in Input) int64 {
	if in.Profile.HoldMs > 0 {
		return in.Profile.HoldMs
	}
	return design.DefaultMotion().HoldMs
}

func isSentencePunct(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?':
		return true
	default:
		return false
	}
}

func splitAll(in Input) (pieces []piece, source string) {
	source = animation.BeatSourcePunct
	hold := holdMsOf(in)
	sections := sectionsFromElements(in.Elements)
	for _, seg := range in.Segments {
		if trimText(seg.Text) == "" {
			continue
		}
		refs := sectionRefCuts([]rune(seg.Text), sections)
		if len(refs) == 0 {
			refs = ordinalCuts([]rune(seg.Text))
		}
		if charsUsable(seg) {
			source = animation.BeatSourceVAD
			pieces = append(pieces, splitByChars(seg, hold, refs)...)
			continue
		}
		pieces = append(pieces, splitByPunct(seg, hold, refs)...)
	}
	return pieces, source
}

type charCut struct {
	at        int
	t         int64
	hard      bool
	section   int
	sectionID string
}

func ordinalCuts(runes []rune) []charCut {
	text := string(runes)
	var out []charCut
	for _, loc := range ordinalClauseRe.FindAllStringIndex(text, -1) {
		at := utf8.RuneCountInString(text[:loc[0]])
		if at <= 0 || at >= len(runes) {
			continue
		}
		out = append(out, charCut{at: at, t: 0, hard: true})
	}
	return out
}

func splitByChars(seg SpeechSeg, hold int64, refs []charCut) []piece {
	runes := []rune(seg.Text)
	n := len(runes)
	if n == 0 {
		return nil
	}
	var cuts []charCut
	seen := map[int]struct{}{}
	add := func(at int, hard bool, section int, sectionID string) {
		if at <= 0 || at >= n {
			return
		}
		if _, ok := seen[at]; ok {
			if hard {
				for k := range cuts {
					if cuts[k].at == at {
						cuts[k].hard = true
					}
				}
			}
			if section > 0 {
				for k := range cuts {
					if cuts[k].at == at {
						cuts[k].section = section
						cuts[k].sectionID = sectionID
					}
				}
			}
			return
		}
		seen[at] = struct{}{}
		cuts = append(cuts, charCut{at: at, t: seg.Chars[at].StartMs, hard: hard, section: section, sectionID: sectionID})
	}
	for i := 0; i < n-1; i++ {
		if isSentencePunct(runes[i]) {
			add(i+1, false, 0, "")
		}
		gap := seg.Chars[i+1].StartMs - seg.Chars[i].EndMs
		if gap >= hold {
			add(i+1, false, 0, "")
		}
	}
	for _, c := range refs {
		add(c.at, c.hard, c.section, c.sectionID)
	}
	sortCuts(cuts)
	cuts = mergeCloseCuts(cuts, hold)

	out := make([]piece, 0, len(cuts)+1)
	hardAt := make(map[int]struct{}, len(cuts))
	sectionAt := make(map[int]charCut, len(cuts))
	for _, c := range cuts {
		if c.hard {
			hardAt[c.at] = struct{}{}
		}
		if c.section > 0 {
			sectionAt[c.at] = c
		}
	}
	lo := 0
	loHard := false
	for _, c := range cuts {
		if p, ok := pieceFromRunes(seg, runes, lo, c.at, sectionAt[lo]); ok {
			p.NoMerge = loHard || c.hard
			out = append(out, p)
		}
		lo = c.at
		_, loHard = hardAt[c.at]
	}
	if p, ok := pieceFromRunes(seg, runes, lo, n, sectionAt[lo]); ok {
		p.NoMerge = loHard
		out = append(out, p)
	}
	return mergeShortPieces(out, hold)
}

func sortCuts(cuts []charCut) {
	for i := 1; i < len(cuts); i++ {
		j := i
		for j > 0 && cuts[j].at < cuts[j-1].at {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
			j--
		}
	}
}

func mergeCloseCuts(cuts []charCut, hold int64) []charCut {
	if len(cuts) < 2 {
		return cuts
	}
	out := cuts[:0]
	for _, c := range cuts {
		if len(out) > 0 && c.t-out[len(out)-1].t < hold && !c.hard && !out[len(out)-1].hard {
			out[len(out)-1] = c
			continue
		}
		out = append(out, c)
	}
	return out
}

func pieceFromRunes(seg SpeechSeg, runes []rune, lo, hi int, cut charCut) (piece, bool) {
	if lo >= hi {
		return piece{}, false
	}
	text := string(runes[lo:hi])
	if trimText(text) == "" {
		return piece{}, false
	}
	start := seg.Chars[lo].StartMs
	end := seg.Chars[hi-1].EndMs
	if end < start {
		end = start
	}
	return piece{
		Text:      text,
		StartMs:   start,
		EndMs:     end,
		SegmentID: seg.ID,
		Section:   cut.section,
		SectionID: cut.sectionID,
	}, true
}

func splitByPunct(seg SpeechSeg, hold int64, refs []charCut) []piece {
	runes := []rune(seg.Text)
	n := len(runes)
	if n == 0 {
		return nil
	}
	bounds := []int{0}
	hardAt := map[int]struct{}{}
	sectionAt := map[int]charCut{}
	addBound := func(at int, hard bool, section int, sectionID string) {
		if at <= 0 || at >= n {
			return
		}
		if at == bounds[len(bounds)-1] {
			if hard {
				hardAt[at] = struct{}{}
			}
			if section > 0 {
				sectionAt[at] = charCut{at: at, section: section, sectionID: sectionID}
			}
			return
		}
		bounds = append(bounds, at)
		if hard {
			hardAt[at] = struct{}{}
		}
		if section > 0 {
			sectionAt[at] = charCut{at: at, section: section, sectionID: sectionID}
		}
	}
	for i := 0; i < n-1; i++ {
		if isSentencePunct(runes[i]) {
			addBound(i+1, false, 0, "")
		}
	}
	for _, c := range refs {
		addBound(c.at, c.hard, c.section, c.sectionID)
	}
	if bounds[len(bounds)-1] != n {
		bounds = append(bounds, n)
	}
	sort.Ints(bounds)
	uniq := bounds[:0]
	for _, b := range bounds {
		if len(uniq) == 0 || uniq[len(uniq)-1] != b {
			uniq = append(uniq, b)
		}
	}
	bounds = uniq

	type raw struct {
		text      string
		weight    float64
		section   int
		sectionID string
	}
	var parts []raw
	for i := 0; i < len(bounds)-1; i++ {
		text := string(runes[bounds[i]:bounds[i+1]])
		if trimText(text) == "" {
			continue
		}
		cut := sectionAt[bounds[i]]
		parts = append(parts, raw{text: text, weight: textWeight(text), section: cut.section, sectionID: cut.sectionID})
	}
	if len(parts) == 0 {
		return nil
	}

	dur := seg.EndMs - seg.StartMs
	if dur < 0 {
		dur = 0
	}
	totalW := 0.0
	for _, p := range parts {
		totalW += p.weight
	}
	if totalW <= 0 {
		totalW = float64(len(parts))
	}

	out := make([]piece, 0, len(parts))
	t := seg.StartMs
	for i, p := range parts {
		var slice int64
		if i == len(parts)-1 {
			slice = seg.EndMs - t
		} else {
			slice = int64(float64(dur) * (p.weight / totalW))
		}
		end := t + slice
		if end < t {
			end = t
		}
		_, hardStart := hardAt[bounds[i]]
		_, hardEnd := hardAt[bounds[i+1]]
		out = append(out, piece{
			Text:      p.text,
			StartMs:   t,
			EndMs:     end,
			SegmentID: seg.ID,
			Section:   p.section,
			SectionID: p.sectionID,
			NoMerge:   hardStart || hardEnd,
		})
		t = end
	}
	if len(out) > 0 {
		out[len(out)-1].EndMs = seg.EndMs
	}
	return mergeShortPieces(out, hold)
}

func textWeight(s string) float64 {
	w := 0.0
	for _, r := range s {
		w += runeWeight(r)
	}
	if w <= 0 {
		return 0.05
	}
	return w
}

func runeWeight(r rune) float64 {
	switch {
	case unicode.Is(unicode.Han, r):
		return 1
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return 0.55
	default:
		return 0.05
	}
}

func mergeShortPieces(pieces []piece, hold int64) []piece {
	if len(pieces) < 2 {
		return pieces
	}
	out := append([]piece(nil), pieces...)
	for len(out) > 1 {
		merged := false
		for i := 0; i < len(out); i++ {
			if out[i].EndMs-out[i].StartMs >= hold {
				continue
			}
			if i == 0 {
				if out[0].NoMerge || out[1].NoMerge {
					continue
				}
				out[1].StartMs = out[0].StartMs
				out[1].Text = out[0].Text + out[1].Text
				out = out[1:]
			} else {
				if out[i-1].NoMerge || out[i].NoMerge {
					continue
				}
				out[i-1].EndMs = out[i].EndMs
				out[i-1].Text += out[i].Text
				out = append(out[:i], out[i+1:]...)
			}
			merged = true
			break
		}
		if !merged {
			break
		}
	}
	return out
}
