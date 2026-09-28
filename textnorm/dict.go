package textnorm

import "regexp"

// LiteralEntry is one exact-match dictionary replacement, the migration target
// for PPTS pronunciation dictionaries (V3.1 §8.1 / §14.1).
type LiteralEntry struct {
	Match   string // non-empty literal to match
	Replace string // replacement text; empty means delete
	Whole   bool   // require non-ASCII-alnum boundaries on both sides
}

// LiteralDictRule converts literal dictionary entries into candidates over the
// original text. Unlike the old span-level ReplaceAll dictionary adapter, each
// occurrence is an independent candidate range and replacements never cascade
// into later rule input.
func LiteralDictRule(id string, priority int, entries []LiteralEntry) CandidateRule {
	patterns := make([]*regexp.Regexp, 0, len(entries))
	for _, e := range entries {
		if e.Match == "" {
			panic("textnorm: LiteralDictRule requires non-empty Match")
		}
		patterns = append(patterns, regexp.MustCompile(regexp.QuoteMeta(e.Match)))
	}
	return &literalDictRule{id: id, priority: priority, entries: entries, patterns: patterns}
}

type literalDictRule struct {
	id       string
	priority int
	entries  []LiteralEntry
	patterns []*regexp.Regexp
}

func (r *literalDictRule) RuleID() string    { return r.id }
func (r *literalDictRule) RulePriority() int { return r.priority }

func (r *literalDictRule) FindCandidates(req Request) []Candidate {
	var out []Candidate
	for i, e := range r.entries {
		for _, loc := range r.patterns[i].FindAllStringIndex(req.Text, -1) {
			if loc[0] == loc[1] {
				continue
			}
			if e.Whole && !asciiBoundary(req.Text, loc[0], loc[1]) {
				continue
			}
			out = append(out, Candidate{
				Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
				Replacement: e.Replace,
				RuleID:      r.id,
				Category:    "dictionary",
				Priority:    r.priority,
			})
		}
	}
	return out
}
