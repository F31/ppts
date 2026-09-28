package textnorm

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mode controls whether a recognized span is converted or only preserved.
type Mode int

const (
	ModeConvert Mode = iota
	ModePreserve
)

// ZHDateRule recognizes yyyy-mm-dd, yyyy/mm/dd, and yyyy.mm.dd date forms.
// In preserve mode it protects the whole date span without changing output.
func ZHDateRule(mode Mode) CandidateRule {
	return zhDateRule{mode: mode}
}

type zhDateRule struct {
	mode Mode
}

var zhDateRe = regexp.MustCompile(`[0-9]{4}[-/.][0-9]{1,2}[-/.][0-9]{1,2}`)

func (r zhDateRule) RuleID() string {
	if r.mode == ModePreserve {
		return "zh.date.preserve"
	}
	return "zh.date"
}

func (r zhDateRule) RulePriority() int { return 10 }

func (r zhDateRule) FindCandidates(req Request) []Candidate {
	locs := zhDateRe.FindAllStringIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhDateToSpeech(matched)
		if !ok {
			continue
		}
		c := Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			Replacement: repl,
			RuleID:      r.RuleID(),
			Category:    "date",
			Priority:    r.RulePriority(),
		}
		if r.mode == ModePreserve {
			c.Preserve = true
			c.Replacement = ""
		}
		out = append(out, c)
	}
	return out
}

// ZHPercentRule converts percent forms such as 13.5% to 百分之十三点五.
func ZHPercentRule() CandidateRule {
	return zhPercentRule{}
}

type zhPercentRule struct{}

var zhPercentRe = regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?%`)

func (zhPercentRule) RuleID() string    { return "zh.percent" }
func (zhPercentRule) RulePriority() int { return 12 }

func (zhPercentRule) FindCandidates(req Request) []Candidate {
	locs := zhPercentRe.FindAllStringIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhPercentToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			Replacement: repl,
			RuleID:      "zh.percent",
			Category:    "percent",
			Priority:    12,
		})
	}
	return out
}

// ZHUnitRule converts common digit-led unit forms such as 5GHz and 3.5GB.
func ZHUnitRule() CandidateRule {
	return zhUnitRule{}
}

type zhUnitRule struct{}

var zhUnitRe = regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?(?:GHz|MHz|kHz|Hz|TB|GB|MB|KB|ms|s|kg|g|km|m)`)

func (zhUnitRule) RuleID() string    { return "zh.unit" }
func (zhUnitRule) RulePriority() int { return 15 }

func (zhUnitRule) FindCandidates(req Request) []Candidate {
	locs := zhUnitRe.FindAllStringIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		if !asciiBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhUnitToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			Replacement: repl,
			RuleID:      "zh.unit",
			Category:    "unit",
			Priority:    15,
		})
	}
	return out
}

// ZHDecimalRule converts simple decimal forms such as 13.5 to 十三点五.
func ZHDecimalRule() CandidateRule {
	return zhDecimalRule{}
}

type zhDecimalRule struct{}

var zhDecimalRe = regexp.MustCompile(`[+-]?[0-9]+\.[0-9]+`)

func (zhDecimalRule) RuleID() string    { return "zh.decimal" }
func (zhDecimalRule) RulePriority() int { return 20 }
func (zhDecimalRule) FindCandidates(req Request) []Candidate {
	locs := zhDecimalRe.FindAllStringIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhDecimalToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			Replacement: repl,
			RuleID:      "zh.decimal",
			Category:    "decimal",
			Priority:    20,
		})
	}
	return out
}

func zhDateToSpeech(s string) (string, bool) {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '/' || r == '.' })
	if len(parts) != 3 || len(parts[0]) != 4 {
		return "", false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", false
	}
	month, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", false
	}
	day, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return "", false
	}
	y, ok := zhYearToSpeech(parts[0])
	if !ok {
		return "", false
	}
	m, ok := zhIntegerToSpeech(parts[1])
	if !ok {
		return "", false
	}
	d, ok := zhIntegerToSpeech(parts[2])
	if !ok {
		return "", false
	}
	return y + "年" + m + "月" + d + "日", true
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

func zhPercentToSpeech(s string) (string, bool) {
	num := strings.TrimSuffix(s, "%")
	spoken, ok := zhNumberToSpeech(num)
	if !ok {
		return "", false
	}
	return "百分之" + spoken, true
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

func zhNumberToSpeech(s string) (string, bool) {
	if strings.Contains(s, ".") {
		return zhDecimalToSpeech(s)
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = strings.TrimPrefix(s, "-")
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	spoken, ok := zhIntegerToSpeech(s)
	if !ok {
		return "", false
	}
	if neg {
		return "负" + spoken, true
	}
	return spoken, true
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

func zhYearToSpeech(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	var sb strings.Builder
	for _, d := range s {
		if d < '0' || d > '9' {
			return "", false
		}
		sb.WriteRune(zhDigit(d))
	}
	return sb.String(), true
}

func zhIntegerToSpeech(s string) (string, bool) {
	if s == "" || len(s) > 12 {
		return "", false
	}
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" {
		return "零", true
	}
	for _, d := range trimmed {
		if d < '0' || d > '9' {
			return "", false
		}
	}
	return zhQuantityWords([]rune(trimmed)), true
}

func zhQuantityWords(ds []rune) string {
	n := len(ds)
	blocks := (n + 3) / 4
	blockName := map[int]string{1: "万", 2: "亿"}
	var out []rune
	pendingZero := false
	wroteAny := false
	for bi := blocks - 1; bi >= 0; bi-- {
		lo := n - (bi+1)*4
		if lo < 0 {
			lo = 0
		}
		hi := n - bi*4
		blockWrote := false
		for i := lo; i < hi; i++ {
			v := int(ds[i] - '0')
			posWithin := hi - i - 1
			if v == 0 {
				if wroteAny {
					pendingZero = true
				}
				continue
			}
			if pendingZero {
				out = append(out, '零')
				pendingZero = false
			}
			if !(len(out) == 0 && v == 1 && posWithin == 1) {
				out = append(out, zhDigit(rune('0'+v)))
			}
			switch posWithin {
			case 3:
				out = append(out, '千')
			case 2:
				out = append(out, '百')
			case 1:
				out = append(out, '十')
			}
			blockWrote = true
			wroteAny = true
		}
		if blockWrote {
			if name, ok := blockName[bi]; ok {
				out = append(out, []rune(name)...)
			}
		}
	}
	if len(out) == 0 {
		return "零"
	}
	return string(out)
}

func zhDigit(d rune) rune {
	switch d {
	case '0':
		return '零'
	case '1':
		return '一'
	case '2':
		return '二'
	case '3':
		return '三'
	case '4':
		return '四'
	case '5':
		return '五'
	case '6':
		return '六'
	case '7':
		return '七'
	case '8':
		return '八'
	case '9':
		return '九'
	}
	return d
}
