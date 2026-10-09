package zh

import (
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// NumberRule converts independent integer tokens (not adjacent to ASCII
// alphanumerics) to their Chinese reading, up to 12 digits. Digit runs bounded
// by ASCII letters/digits are left untouched so model/unit forms such as
// RTX5090Ti and 5GHz are not bisected (V3.1 §6.3 token 隔离).
func NumberRule(mode contract.NumberMode) contract.CandidateRule {
	return numberRule{mode: mode}
}

type numberRule struct{ mode contract.NumberMode }

func (r numberRule) RuleID() string {
	if r.mode == contract.NumberModeYear {
		return "zh.number.year"
	}
	return "zh.number.quantity"
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
			if spoken, ok := numberToSpeech(string(runes[i:j]), r.mode); ok {
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

func numberToSpeech(digits string, mode contract.NumberMode) (string, bool) {
	if len(digits) > 12 {
		return "", false
	}
	if mode == contract.NumberModeYear {
		trimmed := strings.TrimLeft(digits, "0")
		if trimmed == "" {
			return "零", true
		}
		var sb []rune
		for _, d := range trimmed {
			sb = append(sb, zhDigit(d))
		}
		return string(sb), true
	}
	return zhIntegerToSpeech(digits)
}
