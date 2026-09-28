package textnorm

import (
	"regexp"
	"strings"
)

// ZHMoneyRule converts money expressions such as ¥250 / USD 1.5 / 5万 to a
// Chinese reading (V3.1 S4 money category, mirroring the legacy MoneyPattern).
// Priority 13 is before decimal so ¥13.5 is not split by the decimal rule.
func ZHMoneyRule() CandidateRule {
	return zhMoneyRule{}
}

type zhMoneyRule struct{}

var zhMoneyRe = regexp.MustCompile(
	`(?:(?:¥|￥|\$|A\$|HKD|USD|港币|人民币)\s*[+-]?[0-9]+(?:\.[0-9]+)?(?:万)?|[+-]?[0-9]+(?:\.[0-9]+)?万)`,
)

func (zhMoneyRule) RuleID() string    { return "zh.money" }
func (zhMoneyRule) RulePriority() int { return 13 }

func (zhMoneyRule) FindCandidates(req Request) []Candidate {
	locs := zhMoneyRe.FindAllStringIndex(req.Text, -1)
	var out []Candidate
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhMoneyToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
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
