package zh

import (
	"regexp"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// DecimalRule converts simple decimal forms such as 13.5 to 十三点五.
func DecimalRule() contract.CandidateRule {
	return decimalRule{}
}

type decimalRule struct{}

var decimalRe = regexp.MustCompile(`[+-]?[0-9]+\.[0-9]+`)

func (decimalRule) RuleID() string    { return "zh.decimal" }
func (decimalRule) RulePriority() int { return 20 }

func (decimalRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := decimalRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhDecimalToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      "zh.decimal",
			Category:    "decimal",
			Priority:    20,
		})
	}
	return out
}

func zhDecimalToSpeech(s string) (string, bool) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = strings.TrimPrefix(s, "-")
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	parts := strings.Split(s, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	whole, ok := zhIntegerToSpeech(parts[0])
	if !ok {
		return "", false
	}
	var sb strings.Builder
	if neg {
		sb.WriteString("负")
	}
	sb.WriteString(whole)
	sb.WriteString("点")
	for _, d := range parts[1] {
		if d < '0' || d > '9' {
			return "", false
		}
		sb.WriteRune(zhDigit(d))
	}
	return sb.String(), true
}

// PercentRule converts percent forms such as 13.5% to 百分之十三点五.
func PercentRule() contract.CandidateRule {
	return percentRule{}
}

type percentRule struct{}

var percentRe = regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?%`)

func (percentRule) RuleID() string    { return "zh.percent" }
func (percentRule) RulePriority() int { return 12 }

func (percentRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := percentRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhPercentToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      "zh.percent",
			Category:    "percent",
			Priority:    12,
		})
	}
	return out
}

func zhPercentToSpeech(s string) (string, bool) {
	num := strings.TrimSuffix(s, "%")
	spoken, ok := zhNumberToSpeech(num)
	if !ok {
		return "", false
	}
	return "百分之" + spoken, true
}
