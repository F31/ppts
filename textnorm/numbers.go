package textnorm

import "strings"

// NumberMode controls how an independent integer token is read aloud.
type NumberMode int

const (
	// NumberModeYear reads digits one at a time (2024 → 二零二四).
	NumberModeYear NumberMode = iota
	// NumberModeQuantity reads by decimal place (2024 → 二千零二十四).
	NumberModeQuantity
)

// ZHNumberRule converts independent integer tokens (not adjacent to ASCII
// alphanumerics) to their Chinese reading, up to 12 digits. Digit runs bounded
// by ASCII letters/digits are left untouched so model/unit forms such as
// RTX5090Ti and 5GHz are not bisected (V3.1 §6.3 token 隔离).
func ZHNumberRule(mode NumberMode) CandidateRule {
	return zhNumberRule{mode: mode}
}

type zhNumberRule struct{ mode NumberMode }

func (r zhNumberRule) RuleID() string {
	if r.mode == NumberModeYear {
		return "zh.number.year"
	}
	return "zh.number.quantity"
}

func (r zhNumberRule) RulePriority() int { return 20 }

func (r zhNumberRule) FindCandidates(req Request) []Candidate {
	runes := []rune(req.Text)
	var out []Candidate
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
		leftOK := i == 0 || !asciiAlnumRune(runes[i-1])
		rightOK := j == len(runes) || !asciiAlnumRune(runes[j])
		if leftOK && rightOK {
			if spoken, ok := numberToSpeech(string(runes[i:j]), r.mode); ok {
				out = append(out, Candidate{
					Src:         Range{Start: i, End: j},
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

func numberToSpeech(digits string, mode NumberMode) (string, bool) {
	if len(digits) > 12 {
		return "", false
	}
	if mode == NumberModeYear {
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

// asciiAlnumRune 仅 ASCII 字母/数字视为"型号字符"；汉字与标点不拦截。
func asciiAlnumRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
