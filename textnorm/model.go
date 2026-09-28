package textnorm

import "regexp"

// TechnicalModelRule preserves common letter-led technical model identifiers,
// such as RTX5090Ti and i7-11800H. It intentionally does not match digit-led
// unit forms such as 5GHz; those belong to unit/quantity rules.
func TechnicalModelRule() CandidateRule {
	return technicalModelRule{}
}

type technicalModelRule struct{}

var technicalModelRe = regexp.MustCompile(`[A-Za-z]+[0-9][A-Za-z0-9]*(?:-[A-Za-z0-9]+)*`)

// ZHVersionPreserveRule preserves letter-led dotted version identifiers such as
// v3.1.2 and M3.2.1. It requires a letter prefix so bare year-like sequences
// (for example 2024.01.28) remain available to date rules instead of being
// preserved as versions.
func ZHVersionPreserveRule() CandidateRule {
	return zhVersionPreserveRule{}
}

type zhVersionPreserveRule struct{}

var zhVersionRe = regexp.MustCompile(`(?:[Vv][0-9]+|[A-Za-z]+[0-9]+)(?:\.[0-9]+)+`)

func (zhVersionPreserveRule) RuleID() string    { return "zh.version.preserve" }
func (zhVersionPreserveRule) RulePriority() int { return 4 }

func (zhVersionPreserveRule) FindCandidates(req Request) []Candidate {
	locs := zhVersionRe.FindAllStringIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		if !asciiBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		out = append(out, Candidate{
			Src:      Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			RuleID:   "zh.version.preserve",
			Category: "version",
			Priority: 4,
			Preserve: true,
		})
	}
	return out
}

func (technicalModelRule) RuleID() string    { return "technical.model.preserve" }
func (technicalModelRule) RulePriority() int { return 5 }

func (technicalModelRule) FindCandidates(req Request) []Candidate {
	locs := technicalModelRe.FindAllStringIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		if !asciiBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		out = append(out, Candidate{
			Src:      Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			RuleID:   "technical.model.preserve",
			Category: "technical_model",
			Priority: 5,
			Preserve: true,
		})
	}
	return out
}

func asciiBoundary(s string, start, end int) bool {
	if start > 0 {
		left := s[start-1]
		if asciiAlnumByte(left) || left == '-' {
			return false
		}
	}
	if end < len(s) {
		right := s[end]
		if asciiAlnumByte(right) || right == '-' {
			return false
		}
	}
	return true
}

func asciiAlnumByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
