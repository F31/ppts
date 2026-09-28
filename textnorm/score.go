package textnorm

import (
	"regexp"
	"strings"
)

// ZHScoreRule converts a score/ratio form X:Y to "X比Y" (each side read by
// decimal place). It is hint-gated on SemanticScore: without a matching semantic
// hint the ambiguous "3:2" form is conservatively preserved (V3.1 §6.4/§10.1).
func ZHScoreRule() CandidateRule {
	return zhScoreRule{}
}

type zhScoreRule struct{}

var zhScoreRe = regexp.MustCompile(`[0-9]{1,4}:[0-9]{1,4}`)

func (zhScoreRule) RuleID() string             { return "zh.score" }
func (zhScoreRule) RulePriority() int          { return 16 }
func (zhScoreRule) SemanticGate() SemanticKind { return SemanticScore }

func (zhScoreRule) FindCandidates(req Request) []Candidate {
	locs := zhScoreRe.FindAllStringIndex(req.Text, -1)
	var out []Candidate
	for _, loc := range locs {
		m := req.Text[loc[0]:loc[1]]
		left, right, ok := splitColonRatio(m)
		if !ok {
			continue
		}
		ls, ok1 := zhIntegerToSpeech(left)
		rs, ok2 := zhIntegerToSpeech(right)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			Replacement: ls + "比" + rs,
			RuleID:      "zh.score",
			Category:    "score",
			Priority:    16,
		})
	}
	return out
}

func splitColonRatio(s string) (string, string, bool) {
	idx := strings.IndexByte(s, ':')
	if idx <= 0 || idx == len(s)-1 {
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}
