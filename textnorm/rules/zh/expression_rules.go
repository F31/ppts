package zh

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/F31/textnorm/internal/contract"
)

// FractionRule reads a hinted numerator/denominator as 分母分之分子.
// Slash notation is ambiguous with dates and paths, so a fully covering
// SemanticFraction hint is required.
func FractionRule() contract.CandidateRule {
	return gatedExpressionRule{
		expressionRule{
			id: "zh.fraction", category: "fraction", priority: 14,
			re: fractionRe, convert: fractionSpeech, sep: '/',
		},
		contract.SemanticFraction,
	}
}

// TimeRule reads a hinted 24-hour HH:mm[:ss] clock time.
func TimeRule() contract.CandidateRule {
	return gatedExpressionRule{
		expressionRule{
			id: "zh.time", category: "time", priority: 14,
			re: timeRe, convert: timeSpeech, sep: ':',
		},
		contract.SemanticTime,
	}
}

// OrdinalRule reads explicit ordinal forms and normalizes them to the Chinese
// 「第N」 reading: 第12 → 第十二, No.3 → 第三, #2 → 第二.
//
// 为什么 No./# 也算序数（2026-11-02）：中文技术文案里「No.3」「#2」与「第3」是同一处
// 序号的三种写法。不识别时数字会落到 NumberRule 上，读成「No.三」——那是念错，不是
// 「不转换」；同时它也让校验器与读音侧对「什么算序号」的认知对齐。
//
// 守卫（都有测试）：
//   - `#` 只接 1–2 位数字：`#2024` 更像话题标签或年份，读成「第二千零二十四」是误读；
//   - `第 3 章` 里数字与汉字间的空格只在「后面紧跟汉字」时并入候选，得到「第三章」；
//     `第3 ABC` 的中英间隔保留，不把两个词粘成一个；
//   - 沿用引擎的 ASCII 边界检查：`C#2`、`piano.3` 里的标记不当作独立序号。
func OrdinalRule() contract.CandidateRule { return ordinalRule{re: ordinalRe} }

type ordinalRule struct{ re *regexp.Regexp }

func (r ordinalRule) RuleID() string    { return "zh.ordinal" }
func (r ordinalRule) RulePriority() int { return 14 }

func (r ordinalRule) FindCandidates(req contract.Request) []contract.Candidate {
	var out []contract.Candidate
	for _, loc := range r.re.FindAllStringIndex(req.Text, -1) {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		prefix, digits, ok := splitOrdinal(req.Text[loc[0]:loc[1]])
		if !ok {
			continue
		}
		if prefix == "#" && len(digits) > 2 {
			continue
		}
		spoken, ok := zhIntegerToSpeech(digits)
		if !ok {
			continue
		}
		end := loc[1]
		if prefix == "第" {
			end = absorbHanGap(req.Text, end)
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(end)},
			Replacement: "第" + spoken,
			RuleID:      r.RuleID(),
			Category:    "ordinal",
			Priority:    r.RulePriority(),
		})
	}
	return out
}

// splitOrdinal 把匹配到的原文拆成「标记 + 数字」。三种标记都归一到「第」的读法，
// 标记本身不朗读（「No.3」念「第三」，不念「No.三」）。
func splitOrdinal(raw string) (prefix, digits string, ok bool) {
	switch {
	case strings.HasPrefix(raw, "第"):
		prefix, digits = "第", strings.TrimLeft(raw[len("第"):], " \t")
	case len(raw) >= 3 && strings.EqualFold(raw[:3], "no."):
		prefix, digits = "No.", strings.TrimLeft(raw[3:], " \t")
	case strings.HasPrefix(raw, "#"):
		prefix, digits = "#", strings.TrimLeft(raw[1:], " \t")
	default:
		return "", "", false
	}
	if digits == "" {
		return "", "", false
	}
	return prefix, digits, true
}

// absorbHanGap 把数字与后一个汉字之间的空格并入候选区间（「第 3 章」→「第三章」）。
// 后一个字符不是汉字时原样返回，保留「第3 ABC」这种中英间隔。
func absorbHanGap(s string, end int) int {
	i := end
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i == end || i >= len(s) {
		return end
	}
	if r, _ := utf8.DecodeRuneInString(s[i:]); !unicode.Is(unicode.Han, r) {
		return end
	}
	return i
}

// EnglishMoneyRule reads English monetary magnitudes in Chinese.
// Only explicitly prefixed currencies are accepted; bare 500K is not assumed money.
func EnglishMoneyRule() contract.CandidateRule {
	return expressionRule{
		id: "zh.money.english", category: "money", priority: 13,
		re: englishMoneyRe, convert: englishMoneySpeech,
	}
}

var (
	fractionRe     = regexp.MustCompile(`[0-9]+/[0-9]+`)
	timeRe         = regexp.MustCompile(`[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?`)
	ordinalRe      = regexp.MustCompile(`(?:第|(?i:no\.)|#)[ \t]*[0-9]+`)
	englishMoneyRe = regexp.MustCompile(`(?:A\$|HKD|USD|\$)\s*[+-]?[0-9]+(?:\.[0-9]+)?\s*(?i:thousand|million|billion|K|M|B)`)
)

func fractionSpeech(s string) (string, bool) {
	p := strings.Split(s, "/")
	n, nok := zhIntegerToSpeech(p[0])
	d, dok := zhIntegerToSpeech(p[1])
	return d + "分之" + n, nok && dok && d != "零"
}

func timeSpeech(s string) (string, bool) {
	p := strings.Split(s, ":")
	vals := make([]int, len(p))
	for i, part := range p {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || (i == 0 && n > 23) || (i > 0 && n > 59) {
			return "", false
		}
		vals[i] = n
	}
	h, _ := zhIntegerToSpeech(p[0])
	if len(p) == 2 && vals[1] == 0 {
		return h + "点整", true
	}
	m, _ := zhIntegerToSpeech(p[1])
	spoken := h + "点" + m + "分"
	if len(p) == 3 {
		sec, _ := zhIntegerToSpeech(p[2])
		spoken += sec + "秒"
	}
	return spoken, true
}

func englishMoneySpeech(s string) (string, bool) {
	cur := ""
	for _, prefix := range []string{"A$", "HKD", "USD", "$"} {
		if strings.HasPrefix(s, prefix) {
			cur = prefix
			s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
			break
		}
	}
	s = strings.ToLower(s)
	power := 0
	for _, scale := range []struct {
		suffix string
		power  int
	}{
		{"thousand", 3}, {"million", 6}, {"billion", 9}, {"k", 3}, {"m", 6}, {"b", 9},
	} {
		if strings.HasSuffix(s, scale.suffix) {
			power = scale.power
			s = strings.TrimSpace(strings.TrimSuffix(s, scale.suffix))
			break
		}
	}
	if cur == "" || power == 0 {
		return "", false
	}
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
	}
	s = strings.TrimLeft(s, "+-")
	p := strings.SplitN(s, ".", 2)
	digits, fractional := p[0], 0
	if len(p) == 2 {
		digits += p[1]
		fractional = len(p[1])
	}
	// Decimal shift uses strings, avoiding floating-point rounding of money.
	shift := power - fractional
	if shift >= 0 {
		s = digits + strings.Repeat("0", shift)
	} else {
		pos := len(digits) + shift
		if pos <= 0 {
			s = "0." + strings.Repeat("0", -pos) + digits
		} else {
			s = digits[:pos] + "." + digits[pos:]
		}
	}
	n, ok := zhNumberToSpeech(sign + s)
	return moneyCurrencyName(cur) + n, ok
}
