package textnorm

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// selectCandidates picks non-overlapping candidates in priority order and
// records skipped conflicts. Protected ranges block any overlapping candidate.
func selectCandidates(candidates []Candidate, protected []Range) ([]Candidate, []Diagnostic) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Src.Start != b.Src.Start {
			return a.Src.Start < b.Src.Start
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if la, lb := a.Src.End-a.Src.Start, b.Src.End-b.Src.Start; la != lb {
			return la > lb
		}
		return a.RuleID < b.RuleID
	})
	selected := make([]Candidate, 0, len(candidates))
	diagnostics := []Diagnostic{}
	lastEnd := 0
	for _, c := range candidates {
		// protected 已规范化并按起点排序；找到第一个结束位置越过候选起点的区间。
		pi := sort.Search(len(protected), func(i int) bool { return protected[i].End > c.Src.Start })
		if pi < len(protected) && protected[pi].Start < c.Src.End {
			diagnostics = append(diagnostics, Diagnostic{Range: c.Src, Code: "protected", Message: "candidate overlaps protected range", RuleID: c.RuleID})
			continue
		}
		if c.Src.Start < lastEnd {
			diagnostics = append(diagnostics, Diagnostic{Range: c.Src, Code: "conflict", Message: "candidate overlaps selected candidate", RuleID: c.RuleID})
			continue
		}
		selected = append(selected, c)
		lastEnd = c.Src.End
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Src.Start < selected[j].Src.Start })
	return selected, diagnostics
}

// buildOutput builds the normalized text plus source spans and edits from the
// selected candidates in source order.
func buildOutput(src []rune, editsIn []Candidate) (string, []SourceSpan, []Edit) {
	var sb strings.Builder
	spans := make([]SourceSpan, 0, len(editsIn)*2+1)
	edits := make([]Edit, 0, len(editsIn))
	srcCursor, dstCursor := 0, 0
	for _, c := range editsIn {
		if srcCursor < c.Src.Start {
			gap := string(src[srcCursor:c.Src.Start])
			sb.WriteString(gap)
			gapLen := c.Src.Start - srcCursor
			spans = append(spans, SourceSpan{Src: Range{Start: srcCursor, End: c.Src.Start}, Dst: Range{Start: dstCursor, End: dstCursor + gapLen}, Kind: SourceIdentity})
			dstCursor += gapLen
		}
		if c.Preserve {
			preserved := string(src[c.Src.Start:c.Src.End])
			sb.WriteString(preserved)
			preservedLen := c.Src.End - c.Src.Start
			spans = append(spans, SourceSpan{Src: c.Src, Dst: Range{Start: dstCursor, End: dstCursor + preservedLen}, Kind: SourceIdentity, RuleID: c.RuleID})
			dstCursor += preservedLen
			srcCursor = c.Src.End
			continue
		}
		dstStart := dstCursor
		sb.WriteString(c.Replacement)
		replLen := utf8.RuneCountInString(c.Replacement)
		dstCursor += replLen
		kind := SourceReplacement
		if replLen == 0 {
			kind = SourceDeletion
		}
		spans = append(spans, SourceSpan{Src: c.Src, Dst: Range{Start: dstStart, End: dstCursor}, Kind: kind, RuleID: c.RuleID})
		edits = append(edits, Edit{Src: c.Src, Dst: Range{Start: dstStart, End: dstCursor}, Replacement: c.Replacement, RuleID: c.RuleID, Category: c.Category})
		srcCursor = c.Src.End
	}
	if srcCursor < len(src) {
		gap := string(src[srcCursor:])
		sb.WriteString(gap)
		gapLen := len(src) - srcCursor
		spans = append(spans, SourceSpan{Src: Range{Start: srcCursor, End: len(src)}, Dst: Range{Start: dstCursor, End: dstCursor + gapLen}, Kind: SourceIdentity})
	}
	return sb.String(), spans, edits
}

// normalizeRanges validates, sorts, and merges overlapping protected ranges.
func normalizeRanges(ranges []Range, max int) ([]Range, error) {
	out := make([]Range, 0, len(ranges))
	for _, r := range ranges {
		if !validRange(r, max) {
			return nil, fmt.Errorf("textnorm: invalid protected range [%d,%d)", r.Start, r.End)
		}
		if r.Start == r.End {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, r := range out {
		if len(merged) == 0 || merged[len(merged)-1].End < r.Start {
			merged = append(merged, r)
			continue
		}
		if r.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = r.End
		}
	}
	return merged, nil
}

func validRange(r Range, max int) bool {
	return r.Start >= 0 && r.Start <= r.End && r.End <= max
}

func overlapsAny(r Range, ranges []Range) bool {
	for _, x := range ranges {
		if r.Start < x.End && x.Start < r.End {
			return true
		}
	}
	return false
}

func selectedRanges(selected []Candidate) []Range {
	ranges := make([]Range, len(selected))
	for i, s := range selected {
		ranges[i] = s.Src
	}
	return ranges
}
