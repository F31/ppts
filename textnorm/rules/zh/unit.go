package zh

import (
	"regexp"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// UnitRule converts common digit-led unit forms such as 5GHz and 3.5GB.
func UnitRule() contract.CandidateRule {
	return unitRule{}
}

type unitRule struct{}

var unitRe = regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?(?:GHz|MHz|kHz|Hz|TB|GB|MB|KB|ms|s|kg|g|km|m)`)

func (unitRule) RuleID() string    { return "zh.unit" }
func (unitRule) RulePriority() int { return 15 }

func (unitRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := unitRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhUnitToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      "zh.unit",
			Category:    "unit",
			Priority:    15,
		})
	}
	return out
}

func zhUnitToSpeech(s string) (string, bool) {
	unitStart := -1
	for i, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '+' && r != '-' {
			unitStart = i
			break
		}
	}
	if unitStart <= 0 {
		return "", false
	}
	num, unit := s[:unitStart], s[unitStart:]
	spoken, ok := zhNumberToSpeech(num)
	if !ok {
		return "", false
	}
	unitSpeech, ok := zhUnitName(unit)
	if !ok {
		return "", false
	}
	return spoken + unitSpeech, true
}

func zhUnitName(unit string) (string, bool) {
	switch unit {
	case "GHz":
		return "吉赫兹", true
	case "MHz":
		return "兆赫兹", true
	case "kHz":
		return "千赫兹", true
	case "Hz":
		return "赫兹", true
	case "TB":
		return "太字节", true
	case "GB":
		return "吉字节", true
	case "MB":
		return "兆字节", true
	case "KB":
		return "千字节", true
	case "ms":
		return "毫秒", true
	case "s":
		return "秒", true
	case "kg":
		return "千克", true
	case "g":
		return "克", true
	case "km":
		return "千米", true
	case "m":
		return "米", true
	}
	return "", false
}

// MoneyRule converts money expressions such as ¥250 / USD 1.5 / 5万 to a
// Chinese reading. Priority 13 is before decimal so ¥13.5 is not split by the
// decimal rule.
func MoneyRule() contract.CandidateRule {
	return moneyRule{}
}

type moneyRule struct{}

var moneyRe = regexp.MustCompile(
	`(?:(?:¥|￥|\$|A\$|HKD|USD|港币|人民币)\s*[+-]?[0-9]+(?:\.[0-9]+)?(?:万)?|[+-]?[0-9]+(?:\.[0-9]+)?万)`,
)

func (moneyRule) RuleID() string    { return "zh.money" }
func (moneyRule) RulePriority() int { return 13 }

func (moneyRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := moneyRe.FindAllStringIndex(req.Text, -1)
	var out []contract.Candidate
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhMoneyToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      "zh.money",
			Category:    "money",
			Priority:    13,
		})
	}
	return out
}

func zhMoneyToSpeech(matched string) (string, bool) {
	m := strings.TrimSpace(matched)
	cur := ""
	rest := m
	for _, c := range []string{"人民币", "港币", "HKD", "USD", "A$", "$", "¥", "￥"} {
		if strings.HasPrefix(rest, c) {
			cur = c
			rest = strings.TrimSpace(strings.TrimPrefix(rest, c))
			break
		}
	}
	if rest == "" {
		return "", false
	}
	wan := false
	if strings.HasSuffix(rest, "万") {
		wan = true
		rest = strings.TrimSuffix(rest, "万")
	}
	cn, ok := zhNumberToSpeech(rest)
	if !ok {
		return "", false
	}
	name := moneyCurrencyName(cur)
	if name == "" {
		name = "人民币"
	}
	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteString(cn)
	if wan {
		sb.WriteString("万")
	}
	return sb.String(), true
}

// moneyCurrencyName 币种符号 → 中文币种名。
func moneyCurrencyName(sym string) string {
	switch sym {
	case "¥", "￥", "人民币":
		return "人民币"
	case "$", "USD":
		return "美元"
	case "A$":
		return "澳元"
	case "HKD", "港币":
		return "港元"
	}
	return ""
}
