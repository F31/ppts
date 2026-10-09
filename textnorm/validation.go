package textnorm

import (
	"fmt"

	"github.com/F31/textnorm/internal/contract"
)

// validateHints checks hint ranges, kinds, and overlap constraints.
func validateHints(hints []Hint, max int, protected []Range) ([]Hint, error) {
	for i, h := range hints {
		if !validRange(h.Range, max) {
			return nil, fmt.Errorf("textnorm: invalid hint range [%d,%d)", h.Range.Start, h.Range.End)
		}
		switch h.Kind {
		case HintReading:
			if h.Text == "" {
				return nil, fmt.Errorf("textnorm: reading hint %d has empty text", i)
			}
		case HintSemantic:
			if !contract.ValidSemanticKind(h.Semantic) {
				return nil, fmt.Errorf("textnorm: unknown semantic hint %q", h.Semantic)
			}
		}
		if overlapsAny(h.Range, protected) {
			return nil, fmt.Errorf("textnorm: hint [%d,%d) overlaps protected range", h.Range.Start, h.Range.End)
		}
		for j := 0; j < i; j++ {
			if overlapsAny(h.Range, []Range{hints[j].Range}) {
				return nil, fmt.Errorf("textnorm: hints %d and %d overlap", j, i)
			}
		}
	}
	return hints, nil
}

// semanticHintFor returns the semantic hint covering a candidate range, if any.
func semanticHintFor(c Candidate, hints []Hint) (SemanticKind, bool) {
	for _, h := range hints {
		if h.Kind == HintSemantic && overlapsAny(c.Src, []Range{h.Range}) {
			return h.Semantic, true
		}
	}
	return "", false
}

// gatedRuleCovered reports whether a semantic hint of the gate kind covers src.
func gatedRuleCovered(src Range, gate SemanticKind, hints []Hint) bool {
	for _, h := range hints {
		if h.Kind == HintSemantic && h.Semantic == gate && overlapsAny(src, []Range{h.Range}) {
			return true
		}
	}
	return false
}
