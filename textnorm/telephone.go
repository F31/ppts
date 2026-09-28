package textnorm

import (
	"regexp"
	"strings"
)

// ZHTelephoneRule converts conservative CN mobile numbers (1[3-9] plus nine
// digits) to a digit-by-digit Chinese reading. It is bounded away from other
// ASCII alphanumerics so model/unit tokens are not consumed, and sits below the
// quantity number rule so an 11-digit mobile is read as digits rather than a
// huge quantity (V3.1 S4 telephone).
func ZHTelephoneRule() CandidateRule {
	return zhTelephoneRule{}
}

type zhTelephoneRule struct{}

var zhTelephoneRe = regexp.MustCompile(`1[3-9][0-9]{9}`)

func (zhTelephoneRule) RuleID() string    { return "zh.telephone" }
func (zhTelephoneRule) RulePriority() int { return 16 }

func (zhTelephoneRule) FindCandidates(req Request) []Candidate {
	locs := zhTelephoneRe.FindAllStringIndex(req.Text, -1)
	var out []Candidate
	for _, loc := range locs {
		if !asciiBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
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
