package en

import (
	"regexp"

	"github.com/F31/textnorm/internal/contract"
)

// expressionRule is the shared machinery for regex-driven English rules that
// convert a whole matched span (money, date, time).
type expressionRule struct {
	id, category string
	priority     int
	re           *regexp.Regexp
	convert      func(string) (string, bool)
	// sep, when non-zero, rejects matches adjacent to that byte so partial
	// tokens (a date slash or a four-component clock) are not consumed.
	sep byte
}

func (r expressionRule) RuleID() string    { return r.id }
func (r expressionRule) RulePriority() int { return r.priority }

func (r expressionRule) FindCandidates(req contract.Request) []contract.Candidate {
	var out []contract.Candidate
	for _, loc := range r.re.FindAllStringIndex(req.Text, -1) {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		src := contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])}
		if r.sep != 0 {
			if loc[0] > 0 && req.Text[loc[0]-1] == r.sep {
				continue
			}
			if loc[1] < len(req.Text) && req.Text[loc[1]] == r.sep {
				continue
			}
		}
		spoken, ok := r.convert(req.Text[loc[0]:loc[1]])
		if !ok {
			// Recognized monetary amounts outside the converter's limits must
			// not fall back to rewriting only the currency prefix/numeric part.
			if r.category == "money" {
				out = append(out, contract.Candidate{Src: src, Preserve: true, RuleID: r.id, Category: r.category, Priority: r.priority})
			}
			continue
		}
		out = append(out, contract.Candidate{Src: src, Replacement: spoken, RuleID: r.id, Category: r.category, Priority: r.priority})
	}
	return out
}

// gatedExpressionRule additionally requires a fully covering semantic hint of
// the given kind before any candidate is emitted (ambiguous forms stay intact).
type gatedExpressionRule struct {
	expressionRule
	gate contract.SemanticKind
}

func (r gatedExpressionRule) SemanticGate() contract.SemanticKind { return r.gate }

func (r gatedExpressionRule) FindCandidates(req contract.Request) []contract.Candidate {
	var out []contract.Candidate
	for _, c := range r.expressionRule.FindCandidates(req) {
		for _, h := range req.Hints {
			if h.Kind == contract.HintSemantic && h.Semantic == r.gate &&
				h.Range.Start <= c.Src.Start && h.Range.End >= c.Src.End {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
