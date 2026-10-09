package en

import (
	"regexp"

	"github.com/F31/textnorm/internal/contract"
)

// NumberRule converts independent integer tokens (not adjacent to ASCII
// alphanumerics) to their English reading, up to 12 digits. Digit runs bounded
// by ASCII letters/digits are left untouched so model/unit forms are not bisected.
func NumberRule(mode contract.ENNumberMode) contract.CandidateRule {
	return numberRule{mode: mode}
}

type numberRule struct{ mode contract.ENNumberMode }

func (r numberRule) RuleID() string {
	switch r.mode {
	case contract.ENNumberModeYear:
		return "en.number.year"
	case contract.ENNumberModeDigits:
		return "en.number.digits"
	default:
		return "en.number.quantity"
	}
}

func (r numberRule) RulePriority() int { return 20 }

func (r numberRule) FindCandidates(req contract.Request) []contract.Candidate {
	runes := []rune(req.Text)
	var out []contract.Candidate
	i := 0
	for i < len(runes) {
		ch := runes[i]
		if ch < '0' || ch > '9' {
			i++
			continue
		}
		j := i
		for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
			j++
		}
		leftOK := i == 0 || !contract.ASCIIAlnumRune(runes[i-1])
		rightOK := j == len(runes) || !contract.ASCIIAlnumRune(runes[j])
		if leftOK && rightOK {
			if spoken, ok := enNumberToSpeech(string(runes[i:j]), int(r.mode)); ok {
				out = append(out, contract.Candidate{
					Src:         contract.Range{Start: i, End: j},
					Replacement: spoken,
					RuleID:      r.RuleID(),
					Category:    "number",
					Priority:    20,
				})
			}
		}
		i = j
	}
	return out
}

var enDecimalRe = regexp.MustCompile(`[+-]?[0-9]+\.[0-9]+`)

// DecimalRule converts simple decimal forms such as 3.14 to "three point one four".
func DecimalRule() contract.CandidateRule {
	return decimalRule{}
}

type decimalRule struct{}

func (decimalRule) RuleID() string    { return "en.decimal" }
func (decimalRule) RulePriority() int { return 20 }

func (decimalRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := enDecimalRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := enDecimalSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      "en.decimal",
			Category:    "decimal",
			Priority:    20,
		})
	}
	return out
}

var enPercentRe = regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?%`)

// PercentRule converts percent forms such as 12.5% to "twelve point five percent".
func PercentRule() contract.CandidateRule {
	return percentRule{}
}

type percentRule struct{}

func (percentRule) RuleID() string    { return "en.percent" }
func (percentRule) RulePriority() int { return 12 }

func (percentRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := enPercentRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := enPercentSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      "en.percent",
			Category:    "percent",
			Priority:    12,
		})
	}
	return out
}
