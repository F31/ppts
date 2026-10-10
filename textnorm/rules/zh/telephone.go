package zh

import (
	"regexp"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// TelephoneRule converts conservative CN mobile numbers (1[3-9] plus nine
// digits) to a digit-by-digit Chinese reading. It is bounded away from other
// ASCII alphanumerics so model/unit tokens are not consumed, and sits below the
// quantity number rule so an 11-digit mobile is read as digits rather than a
// huge quantity.
func TelephoneRule() contract.CandidateRule {
	return telephoneRule{}
}

type telephoneRule struct{}

var telephoneRe = regexp.MustCompile(`1[3-9][0-9]{9}`)

func (telephoneRule) RuleID() string    { return "zh.telephone" }
func (telephoneRule) RulePriority() int { return 16 }

func (telephoneRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := telephoneRe.FindAllStringIndex(req.Text, -1)
	var out []contract.Candidate
	for _, loc := range locs {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: telephoneDigitsToSpeech(req.Text[loc[0]:loc[1]]),
			RuleID:      "zh.telephone",
			Category:    "telephone",
			Priority:    16,
		})
	}
	return out
}

func telephoneDigitsToSpeech(digits string) string {
	var sb strings.Builder
	for _, d := range digits {
		sb.WriteRune(zhDigit(d))
	}
	return sb.String()
}

// ScoreRule converts a score/ratio form X:Y to "X比Y" (each side read by
// decimal place). It is hint-gated on SemanticScore: without a matching semantic
// hint the ambiguous "3:2" form is conservatively preserved.
func ScoreRule() contract.CandidateRule {
	return scoreRule{}
}

type scoreRule struct{}

var scoreRe = regexp.MustCompile(`[0-9]{1,4}:[0-9]{1,4}`)

func (scoreRule) RuleID() string                      { return "zh.score" }
func (scoreRule) RulePriority() int                   { return 16 }
func (scoreRule) SemanticGate() contract.SemanticKind { return contract.SemanticScore }

func (scoreRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := scoreRe.FindAllStringIndex(req.Text, -1)
	var out []contract.Candidate
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
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
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
