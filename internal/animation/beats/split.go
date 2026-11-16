package beats

import (
	"unicode"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

type piece struct {
	Text      string
	StartMs   int64
	EndMs     int64
	SegmentID string
}

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
	for _, seg := range in.Segments {
		if trimText(seg.Text) == "" {
			continue
		}
		if charsUsable(seg) {
			source = animation.BeatSourceVAD
			pieces = append(pieces, splitByChars(seg, hold)...)
			continue
		}
		pieces = append(pieces, splitByPunct(seg, hold)...)
	}
	return pieces, source
}

type charCut struct {
	at int
	t  int64
}

func splitByChars(seg SpeechSeg, hold int64) []piece {
	runes := []rune(seg.Text)
	n := len(runes)
	if n == 0 {
		return nil
	}
	var cuts []charCut
	seen := map[int]struct{}{}
	add := func(at int) {
		if at <= 0 || at >= n {
			return
		}
		if _, ok := seen[at]; ok {
			return
		}
		seen[at] = struct{}{}
		cuts = append(cuts, charCut{at: at, t: seg.Chars[at].StartMs})
	}
	for i := 0; i < n-1; i++ {
		if isSentencePunct(runes[i]) {
			add(i + 1)
		}
		gap := seg.Chars[i+1].StartMs - seg.Chars[i].EndMs
		if gap >= hold {
			add(i + 1)
		}
	}
	sortCuts(cuts)
	cuts = mergeCloseCuts(cuts, hold)

	out := make([]piece, 0, len(cuts)+1)
	lo := 0
	for _, c := range cuts {
		if p, ok := pieceFromRunes(seg, runes, lo, c.at); ok {
			out = append(out, p)
		}
		lo = c.at
	}
	if p, ok := pieceFromRunes(seg, runes, lo, n); ok {
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
		if len(out) > 0 && c.t-out[len(out)-1].t < hold {
			out[len(out)-1] = c
			continue
		}
		out = append(out, c)
	}
	return out
}

func pieceFromRunes(seg SpeechSeg, runes []rune, lo, hi int) (piece, bool) {
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
	}, true
}

func splitByPunct(seg SpeechSeg, hold int64) []piece {
	runes := []rune(seg.Text)
	n := len(runes)
	if n == 0 {
		return nil
	}
	var bounds []int
	bounds = append(bounds, 0)
	for i := 0; i < n-1; i++ {
		if isSentencePunct(runes[i]) {
			at := i + 1
			if at != bounds[len(bounds)-1] {
				bounds = append(bounds, at)
			}
		}
	}
	if bounds[len(bounds)-1] != n {
		bounds = append(bounds, n)
	}

	type raw struct {
		text   string
		weight float64
	}
	var parts []raw
	for i := 0; i < len(bounds)-1; i++ {
		text := string(runes[bounds[i]:bounds[i+1]])
		if trimText(text) == "" {
			continue
		}
		parts = append(parts, raw{text: text, weight: textWeight(text)})
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
		out = append(out, piece{
			Text:      p.text,
			StartMs:   t,
			EndMs:     end,
			SegmentID: seg.ID,
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
				out[1].StartMs = out[0].StartMs
				out[1].Text = out[0].Text + out[1].Text
				out = out[1:]
			} else {
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
