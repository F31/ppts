// Package rules holds language-neutral rules shared by language-specific rule
// sets: literal dictionary entries and declarative regex templates.
package rules

import (
	"regexp"

	"github.com/F31/textnorm/internal/contract"
)

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
func LiteralDictRule(id string, priority int, entries []LiteralEntry) contract.CandidateRule {
	entries = append([]LiteralEntry(nil), entries...)
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

func (r *literalDictRule) FindCandidates(req contract.Request) []contract.Candidate {
	var out []contract.Candidate
	for i, e := range r.entries {
		for _, loc := range r.patterns[i].FindAllStringIndex(req.Text, -1) {
			if loc[0] == loc[1] {
				continue
			}
			if e.Whole && !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
				continue
			}
			out = append(out, contract.Candidate{
				Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
				Replacement: e.Replace,
				RuleID:      r.id,
				Category:    "dictionary",
				Priority:    r.priority,
			})
		}
	}
	return out
}

// RegexRule is a minimal declarative rule for exact-range template replacements.
type RegexRule struct {
	ID          string
	Category    string
	Priority    int
	Match       *regexp.Regexp
	Replacement string
}

func (r RegexRule) RuleID() string    { return r.ID }
func (r RegexRule) RulePriority() int { return r.Priority }

func (r RegexRule) FindCandidates(req contract.Request) []contract.Candidate {
	if r.Match == nil {
		return nil
	}
	locs := r.Match.FindAllStringSubmatchIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		if loc[0] == loc[1] {
			continue
		}
		matched := req.Text[loc[0]:loc[1]]
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: r.Match.ReplaceAllString(matched, r.Replacement),
			RuleID:      r.ID,
			Category:    r.Category,
			Priority:    r.Priority,
		})
	}
	return out
}
