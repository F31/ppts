package en

import (
	"regexp"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// UnitRule converts digit-led unit forms such as 5GHz, 3.5GB and 100 Mbps into
// English speech with correct plurality. Digit runs bounded by other ASCII
// alphanumerics are left intact so model tokens are not bisected. An exact "1"
// takes the singular unit; integers >1, decimals and signed amounts take the
// plural. Uncountable units (hertz, bps) never inflect.
func UnitRule() contract.CandidateRule {
	return unitRule{}
}

type unitRule struct{}

var enUnitRe = regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?\s?(?:GHz|MHz|kHz|Hz|TB|GB|MB|KB|Mbps|Kbps|ms|s|km|kg|g|m|cm|mm|°C|°F)`)

func (unitRule) RuleID() string    { return "en.unit" }
func (unitRule) RulePriority() int { return 15 }

func (unitRule) FindCandidates(req contract.Request) []contract.Candidate {
	runes := []rune(req.Text)
	locs := enUnitRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		startR := req.RuneOffset(loc[0])
		endR := req.RuneOffset(loc[1])
		if startR > 0 && contract.ASCIIAlnumRune(runes[startR-1]) {
			continue
		}
		if endR < len(runes) && contract.ASCIIAlnumRune(runes[endR]) {
			continue
		}
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := enUnitToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: startR, End: endR},
			Replacement: repl,
			RuleID:      "en.unit",
			Category:    "unit",
			Priority:    15,
		})
	}
	return out
}

func enUnitToSpeech(s string) (string, bool) {
	// Split numeric part from unit suffix. Units are matched left-to-right by
	// the longest byte prefix; the number always leads.
	num, unit, ok := splitLeadingNumber(s)
	if !ok {
		return "", false
	}
	amount, ok := enUnitAmountSpeech(num)
	if !ok {
		return "", false
	}
	unitName, pluralizable, ok := enUnitName(unit)
	if !ok {
		return "", false
	}
	if pluralizable && isPluralAmount(num) {
		unitName = enPluralizeUnit(unitName)
	}
	return amount + " " + unitName, true
}

// splitLeadingNumber 从 "3.5GB" 中拆出数字与单位。
func splitLeadingNumber(s string) (num, unit string, ok bool) {
	i := 0
	// 符号
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digitsStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > digitsStart && i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	if i == digitsStart || i >= len(s) {
		return "", "", false
	}
	return s[:i], strings.TrimSpace(s[i:]), true
}

// enUnitAmountSpeech 读数字部分（整数按数量词，小数按 decimal，负号 "-"）。
func enUnitAmountSpeech(num string) (string, bool) {
	neg := false
	if strings.HasPrefix(num, "-") {
		neg = true
		num = strings.TrimPrefix(num, "-")
	} else {
		num = strings.TrimPrefix(num, "+")
	}
	if strings.Contains(num, ".") {
		spoken, ok := enDecimalSpeech(num)
		if !ok {
			return "", false
		}
		if neg {
			return "minus " + spoken, true
		}
		return spoken, true
	}
	spoken, ok := enIntegerToWords(strings.TrimLeft(num, "0"))
	if !ok {
		return "", false
	}
	if neg {
		return "minus " + spoken, true
	}
	return spoken, true
}

// isPluralAmount 判断数量是否为复数（非精确 1，或含小数）。
func isPluralAmount(num string) bool {
	trimmed := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(num, "+"), "-"), "0")
	if strings.Contains(trimmed, ".") {
		return true
	}
	return trimmed != "1"
}

func enUnitName(unit string) (name string, pluralizable bool, ok bool) {
	switch unit {
	case "GHz":
		return "gigahertz", false, true
	case "MHz":
		return "megahertz", false, true
	case "kHz":
		return "kilohertz", false, true
	case "Hz":
		return "hertz", false, true
	case "TB":
		return "terabyte", true, true
	case "GB":
		return "gigabyte", true, true
	case "MB":
		return "megabyte", true, true
	case "KB":
		return "kilobyte", true, true
	case "Mbps":
		return "megabit per second", true, true
	case "Kbps":
		return "kilobit per second", true, true
	case "ms":
		return "millisecond", true, true
	case "s":
		return "second", true, true
	case "km":
		return "kilometer", true, true
	case "kg":
		return "kilogram", true, true
	case "g":
		return "gram", true, true
	case "m":
		return "meter", true, true
	case "cm":
		return "centimeter", true, true
	case "mm":
		return "millimeter", true, true
	case "°C":
		return "degree Celsius", true, true
	case "°F":
		return "degree Fahrenheit", true, true
	}
	return "", false, false
}

func enPluralizeUnit(name string) string {
	// "megabit per second" → "megabits per second"：复数量词的首词。
	if i := strings.Index(name, " "); i >= 0 && strings.HasSuffix(name[:i], "bit") {
		return name[:i] + "s" + name[i:]
	}
	// "degree Celsius" → "degrees Celsius"：degree 复数。
	if strings.HasPrefix(name, "degree ") {
		return "degrees" + name[len("degree"):]
	}
	if strings.HasSuffix(name, "y") {
		return strings.TrimSuffix(name, "y") + "ies"
	}
	return name + "s"
}
