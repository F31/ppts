package zh

import (
	"regexp"
	"strconv"
	"strings"

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

// OrdinalRule reads explicit Chinese ordinal forms such as 第12 → 第十二.
func OrdinalRule() contract.CandidateRule {
	return expressionRule{
		id: "zh.ordinal", category: "ordinal", priority: 14, re: ordinalRe,
		convert: func(s string) (string, bool) {
			n, ok := zhIntegerToSpeech(strings.TrimPrefix(s, "第"))
			return "第" + n, ok
		},
	}
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
	ordinalRe      = regexp.MustCompile(`第[0-9]+`)
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
